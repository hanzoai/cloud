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
	// Status is the screen's state: pending | done | failed.
	Status string `json:"status"`
	// Score is the model's 0..100 fit score, clamped to that range.
	Score int `json:"score"`
	// Tier1Backed is the model's read on tier-1 backing, normalized to
	// "yes", "no" or "unclear" (anything it cannot resolve reads "unclear").
	Tier1Backed string `json:"tier1Backed"`
	// SuggestedCredits is the recommended credit grant in USD, snapped to the
	// nearest allowed rung: 0 | 5000 | 25000 | 50000 | 150000.
	SuggestedCredits int `json:"suggestedCredits"`
	// Summary is the model's short assessment of the application.
	Summary string `json:"summary"`
	// DraftReply is a suggested email reply for staff to edit and send.
	DraftReply string `json:"draftReply"`
	// Model is the LLM the screen ran on.
	Model string `json:"model"`
	// ScreenedAt is the unix second the screen finished (0 while pending).
	ScreenedAt int64 `json:"screenedAt"`
	// Error says why a failed screen failed — no AI gateway configured, a gateway
	// error, or a reply that carried no parseable JSON. Absent on success.
	Error string `json:"error,omitempty"`
}

// StageEvent is one entry in an application's append-only stage-transition log.
type StageEvent struct {
	// From is the stage moved out of; empty on the intake event that opens the log.
	From string `json:"from"`
	// To is the stage moved into.
	To string `json:"to"`
	// At is the unix second of the move.
	At int64 `json:"at"`
	// By is who moved it: "system" for intake and the AI auto-advance, else the
	// validated staff user id.
	By string `json:"by"`
	// Note is the free-text comment recorded with the move. Absent when none.
	Note string `json:"note,omitempty"`
}

// ProgramApplication is one startup-program submission plus its AI screen and pipeline
// state. Metadata carries the FULL submitted payload (all form fields, including
// arrays like tier1Investors/useCases); the promoted columns are query/display
// projections. Tier1 is deterministically derived at intake from the submitted
// fund list (independent of the AI screen's judgement).
type ProgramApplication struct {
	// ID is the server-minted application id ("appl_" + 128 random bits).
	ID string `json:"id"`
	// Org is the owning tenant — the program org, which is the deployment brand.
	// Never on the wire: it is the isolation key the server reads, not a field a
	// caller sends or reads.
	Org string `json:"-"`
	// Company is the applicant's company name.
	Company string `json:"company"`
	// Website is the applicant's website as submitted.
	Website string `json:"website"`
	// ContactName is the person who applied.
	ContactName string `json:"contactName"`
	// Email is the applicant's email — half of the (email, company) key a
	// resubmission refreshes instead of duplicating.
	Email string `json:"email"`
	// Role is the applicant's role at their company.
	Role string `json:"role"`
	// Stage is the pipeline stage: applied, screened, qualified, credits-offered,
	// onboarded or rejected. Server-owned — it starts at "applied" and moves only
	// through the transition machine.
	Stage string `json:"stage"`
	// Tier1 is whether the applicant is tier-1 backed, derived deterministically
	// at intake from the submitted fund list — independent of the AI screen.
	Tier1 bool `json:"tier1"`
	// Metadata is the FULL submitted form, every field, including the arrays the
	// promoted columns above do not carry (tier1Investors, useCases) and the
	// deterministic tier1Matched list.
	Metadata map[string]any `json:"metadata"`
	// Screen is the AI screen. It runs after intake, so a freshly created
	// application carries a "pending" screen.
	Screen ScreenResult `json:"screen"`
	// Events is the append-only stage-transition log, oldest first.
	Events []StageEvent `json:"events"`
	// CompanyID is the CRM Company minted for this lead at intake, so the startup
	// also appears in the org's standard CRM tabs. Empty when that best-effort
	// projection did not run.
	CompanyID string `json:"companyId"`
	// ContactID is the CRM Contact minted for this lead at intake. Empty when that
	// best-effort projection did not run.
	ContactID string `json:"contactId"`
	// Reason is why the application was rejected, required to reject. Empty
	// otherwise.
	Reason string `json:"reason"`
	// CreatedAt is the unix second the application arrived. Server-owned.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second of the last write. Server-owned.
	UpdatedAt int64 `json:"updatedAt"`
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

func scanApplication(sc interface{ Scan(...any) error }) (ProgramApplication, error) {
	var a ProgramApplication
	var tier1 int
	var meta, screen, events string
	err := sc.Scan(&a.ID, &a.Org, &a.Company, &a.Website, &a.ContactName, &a.Email,
		&a.Role, &a.Stage, &tier1, &meta, &screen, &events, &a.CompanyID, &a.ContactID,
		&a.Reason, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return ProgramApplication{}, err
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

func (s *Store) CreateApplication(ctx context.Context, a ProgramApplication) (ProgramApplication, error) {
	meta := jsonOr(a.Metadata, "{}")
	screen := jsonOr(a.Screen, "{}")
	events := jsonOr(a.Events, "[]")
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO crm_applications (`+appCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.Company, a.Website, a.ContactName, a.Email, a.Role, a.Stage,
		b2i(a.Tier1), meta, screen, events, a.CompanyID, a.ContactID, a.Reason,
		a.CreatedAt, a.UpdatedAt); err != nil {
		return ProgramApplication{}, fmt.Errorf("insert application: %w", err)
	}
	return a, nil
}

func (s *Store) GetApplication(ctx context.Context, org, id string) (ProgramApplication, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appCols+` FROM crm_applications WHERE org=? AND id=?`, org, id)
	a, err := scanApplication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProgramApplication{}, errNotFound
	}
	if err != nil {
		return ProgramApplication{}, fmt.Errorf("get application: %w", err)
	}
	return a, nil
}

// ListApplications lists an org's applications, optionally filtered by pipeline
// stage (stage=="" means all). Newest first.
func (s *Store) ListApplications(ctx context.Context, org, stage string, limit int) ([]ProgramApplication, error) {
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
	out := make([]ProgramApplication, 0, 16)
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
func (s *Store) FindApplicationByEmailCompany(ctx context.Context, org, email, company string) (ProgramApplication, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+appCols+` FROM crm_applications
		 WHERE org=? AND lower(email)=lower(?) AND lower(company)=lower(?)
		 ORDER BY created_at DESC LIMIT 1`, org, email, company)
	a, err := scanApplication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProgramApplication{}, errNotFound
	}
	if err != nil {
		return ProgramApplication{}, fmt.Errorf("find application: %w", err)
	}
	return a, nil
}

// UpdateApplication persists the mutable columns (stage, tier1, metadata,
// screen, events, links, reason). ID/Org/CreatedAt are immutable keys.
func (s *Store) UpdateApplication(ctx context.Context, a ProgramApplication) (ProgramApplication, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE crm_applications
		 SET company=?,website=?,contact_name=?,email=?,role=?,stage=?,tier1=?,
		     metadata=?,screen=?,events=?,company_id=?,contact_id=?,reason=?,updated_at=?
		 WHERE org=? AND id=?`,
		a.Company, a.Website, a.ContactName, a.Email, a.Role, a.Stage, b2i(a.Tier1),
		jsonOr(a.Metadata, "{}"), jsonOr(a.Screen, "{}"), jsonOr(a.Events, "[]"),
		a.CompanyID, a.ContactID, a.Reason, a.UpdatedAt, a.Org, a.ID)
	if err != nil {
		return ProgramApplication{}, fmt.Errorf("update application: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ProgramApplication{}, errNotFound
	}
	return s.GetApplication(ctx, a.Org, a.ID)
}

// CountApplications returns the org's application count (overview cards).
func (s *Store) CountApplications(ctx context.Context, org string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crm_applications WHERE org=?`, org).Scan(&n)
	return n, err
}
