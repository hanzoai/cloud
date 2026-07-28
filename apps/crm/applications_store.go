package crm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ---- Startup Program applications ----
//
// The Hanzo Startup Program is Hanzo's OWN inbound-sales pipeline (org "hanzo"):
// a public marketing form (hanzo.ai/startups) posts an application, an AI screen
// scores it, and staff work it through a pipeline in admin.hanzo.ai. It is a
// DEDICATED resource rather than a generic CRM opportunity because the pipeline
// has its own stage vocabulary (applied…rejected — NOT the Twenty sales stages)
// and carries a free-form metadata bag + a stored AI screen the flat opportunity
// schema has no room for. It lives in the same crm.db, one table, org-scoped like
// every other CRM entity (WHERE org=? on every query).

// Startup pipeline stages, in order. `rejected` is an off-pipeline terminal.
const (
	StageApplied        = "applied"
	StageScreened       = "screened"
	StageQualified      = "qualified"
	StageCreditsOffered = "credits-offered"
	StageOnboarded      = "onboarded"
	StageRejected       = "rejected"
)

// stageOrder is the forward progression; index used by the transition machine.
var stageOrder = []string{StageApplied, StageScreened, StageQualified, StageCreditsOffered, StageOnboarded}

// validStages is every legal stage value (stageOrder + rejected).
var validStages = func() map[string]bool {
	m := map[string]bool{StageRejected: true}
	for _, s := range stageOrder {
		m[s] = true
	}
	return m
}()

// ScreenResult is the AI screen stored on an application. Status is
// pending → done | failed; a failed/absent screen never blocks intake.
type ScreenResult struct {
	Status           string `json:"status"` // pending | done | failed
	Score            int    `json:"score"`  // 0..100
	Tier1Backed      string `json:"tier1Backed"`
	SuggestedCredits int    `json:"suggestedCredits"` // 0 | 5000 | 25000 | 50000 | 150000
	Summary          string `json:"summary"`
	DraftReply       string `json:"draftReply"`
	Model            string `json:"model"`
	ScreenedAt       int64  `json:"screenedAt"`
	Error            string `json:"error,omitempty"`
}

// StageEvent is one entry in an application's append-only stage-transition log.
type StageEvent struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   int64  `json:"at"`
	By   string `json:"by"` // "system" or a staff user id
	Note string `json:"note,omitempty"`
}

// Application is one startup-program submission plus its AI screen and pipeline
// state. Metadata carries the FULL submitted payload (all form fields, including
// arrays like tier1Investors/useCases); the promoted columns are query/display
// projections. Tier1 is deterministically derived at intake from the submitted
// fund list (independent of the AI screen's judgement).
type Application struct {
	ID          string         `json:"id"`
	Org         string         `json:"-"`
	Company     string         `json:"company"`
	Website     string         `json:"website"`
	ContactName string         `json:"contactName"`
	Email       string         `json:"email"`
	Role        string         `json:"role"`
	Stage       string         `json:"stage"`
	Tier1       bool           `json:"tier1"`
	Metadata    map[string]any `json:"metadata"`
	Screen      ScreenResult   `json:"screen"`
	Events      []StageEvent   `json:"events"`
	CompanyID   string         `json:"companyId"`
	ContactID   string         `json:"contactId"`
	Reason      string         `json:"reason"`
	CreatedAt   int64          `json:"createdAt"`
	UpdatedAt   int64          `json:"updatedAt"`
}

// migrateApplications creates the crm_applications table. Idempotent; called
// from openStore after the core CRM tables.
func (s *Store) migrateApplications() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS crm_applications (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  company      TEXT NOT NULL DEFAULT '',
  website      TEXT NOT NULL DEFAULT '',
  contact_name TEXT NOT NULL DEFAULT '',
  email        TEXT NOT NULL DEFAULT '',
  role         TEXT NOT NULL DEFAULT '',
  stage        TEXT NOT NULL DEFAULT 'applied',
  tier1        INTEGER NOT NULL DEFAULT 0,
  metadata     TEXT NOT NULL DEFAULT '{}',
  screen       TEXT NOT NULL DEFAULT '{}',
  events       TEXT NOT NULL DEFAULT '[]',
  company_id   TEXT NOT NULL DEFAULT '',
  contact_id   TEXT NOT NULL DEFAULT '',
  reason       TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_crm_apps_org_created ON crm_applications(org, created_at);
CREATE INDEX IF NOT EXISTS ix_crm_apps_org_stage   ON crm_applications(org, stage);
CREATE INDEX IF NOT EXISTS ix_crm_apps_org_email   ON crm_applications(org, email);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("crm applications migrate: %w", err)
	}
	return nil
}

const appCols = `id,org,company,website,contact_name,email,role,stage,tier1,metadata,screen,events,company_id,contact_id,reason,created_at,updated_at`

func scanApplication(sc interface{ Scan(...any) error }) (Application, error) {
	var a Application
	var tier1 int
	var meta, screen, events string
	err := sc.Scan(&a.ID, &a.Org, &a.Company, &a.Website, &a.ContactName, &a.Email,
		&a.Role, &a.Stage, &tier1, &meta, &screen, &events, &a.CompanyID, &a.ContactID,
		&a.Reason, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return Application{}, err
	}
	a.Tier1 = tier1 != 0
	a.Metadata = map[string]any{}
	if meta != "" {
		_ = json.Unmarshal([]byte(meta), &a.Metadata)
	}
	if screen != "" {
		_ = json.Unmarshal([]byte(screen), &a.Screen)
	}
	a.Events = []StageEvent{}
	if events != "" {
		_ = json.Unmarshal([]byte(events), &a.Events)
	}
	return a, err
}

// jsonOr marshals v, returning fallback on error (never persists a broken blob).
func jsonOr(v any, fallback string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}

func (s *Store) CreateApplication(ctx context.Context, a Application) (Application, error) {
	meta := jsonOr(a.Metadata, "{}")
	screen := jsonOr(a.Screen, "{}")
	events := jsonOr(a.Events, "[]")
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO crm_applications (`+appCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.Company, a.Website, a.ContactName, a.Email, a.Role, a.Stage,
		b2i(a.Tier1), meta, screen, events, a.CompanyID, a.ContactID, a.Reason,
		a.CreatedAt, a.UpdatedAt); err != nil {
		return Application{}, fmt.Errorf("insert application: %w", err)
	}
	return a, nil
}

func (s *Store) GetApplication(ctx context.Context, org, id string) (Application, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appCols+` FROM crm_applications WHERE org=? AND id=?`, org, id)
	a, err := scanApplication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, errNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("get application: %w", err)
	}
	return a, nil
}

// ListApplications lists an org's applications, optionally filtered by pipeline
// stage (stage=="" means all). Newest first.
func (s *Store) ListApplications(ctx context.Context, org, stage string, limit int) ([]Application, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if stage == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+appCols+` FROM crm_applications WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+appCols+` FROM crm_applications WHERE org=? AND stage=? ORDER BY created_at DESC LIMIT ?`, org, stage, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Application, 0, 16)
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("scan application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FindApplicationByEmailCompany returns the org's application matching a
// case-insensitive (email, company) pair, or errNotFound. Basis for idempotent
// intake (a resubmission updates rather than duplicates).
func (s *Store) FindApplicationByEmailCompany(ctx context.Context, org, email, company string) (Application, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+appCols+` FROM crm_applications
		 WHERE org=? AND lower(email)=lower(?) AND lower(company)=lower(?)
		 ORDER BY created_at DESC LIMIT 1`, org, email, company)
	a, err := scanApplication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, errNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("find application: %w", err)
	}
	return a, nil
}

// UpdateApplication persists the mutable columns (stage, tier1, metadata,
// screen, events, links, reason). ID/Org/CreatedAt are immutable keys.
func (s *Store) UpdateApplication(ctx context.Context, a Application) (Application, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE crm_applications
		 SET company=?,website=?,contact_name=?,email=?,role=?,stage=?,tier1=?,
		     metadata=?,screen=?,events=?,company_id=?,contact_id=?,reason=?,updated_at=?
		 WHERE org=? AND id=?`,
		a.Company, a.Website, a.ContactName, a.Email, a.Role, a.Stage, b2i(a.Tier1),
		jsonOr(a.Metadata, "{}"), jsonOr(a.Screen, "{}"), jsonOr(a.Events, "[]"),
		a.CompanyID, a.ContactID, a.Reason, a.UpdatedAt, a.Org, a.ID)
	if err != nil {
		return Application{}, fmt.Errorf("update application: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Application{}, errNotFound
	}
	return s.GetApplication(ctx, a.Org, a.ID)
}

// CountApplications returns the org's application count (overview cards).
func (s *Store) CountApplications(ctx context.Context, org string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crm_applications WHERE org=?`, org).Scan(&n)
	return n, err
}
