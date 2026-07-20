package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errNotFound is returned when a session (or its events) is not present in the
// org's file; handlers map it to HTTP 404.
var errNotFound = errors.New("session: not found")

// Session is one live (or ended) coding-agent run launched by `hanzo code
// <agent>`. It is the value console.hanzo.ai lists and drives: the registry row
// that says a claude/codex/dev process is running on some machine, against some
// Hanzo model, in some working directory, reachable at TerminalURL.
//
// Org is the physical tenant boundary (one sessions.db per org, filtered
// WHERE org=? on every query). Project is the LINK to the deployable
// projects.Project the run belongs to (principal.Project, "" = none) — so a
// project view can filter its own sessions without a second registry.
//
// TerminalURL is the ttyd web-terminal endpoint the launcher publishes once its
// ZT (or zrok) tunnel is up; empty until the mirror is ready. Status is the
// lifecycle column the console polls: starting → running → waiting (needs
// input) → ended | error.
type Session struct {
	ID          string
	Org         string
	Project     string
	User        string
	Agent       string // claude | codex | dev
	Model       string
	Host        string
	Cwd         string
	TerminalURL string
	Status      string
	StartedAt   int64
	UpdatedAt   int64
	EndedAt     int64 // 0 while live
}

// Event is one lifecycle signal a session's agent emits — sourced from Claude
// Code's Notification/Stop hooks (needs-input, turn-done) — so the console and
// the @hanzo Slack relay learn state changes without scraping the terminal.
type Event struct {
	ID        string
	SessionID string
	Org       string
	Kind      string // notification | stop | error | log
	Message   string
	CreatedAt int64
}

// Filter narrows a session list. Empty fields do not constrain. Live selects
// only sessions that have not ended (the console's default "what's running").
type Filter struct {
	Status  string
	Project string
	Host    string
	Agent   string
	Live    bool
}

// Store is one org's SQLite file. Every method filters WHERE org=?, so a query
// in one org's store can never reach another org's rows.
type Store struct{ db *sql.DB }

// Close releases the org's SQLite handle; the OrgStore cache calls it on
// eviction and on Shutdown.
func (s *Store) Close() error { return s.db.Close() }

const sessionCols = "id, org, project, usr, agent, model, host, cwd, terminal_url, status, started_at, updated_at, ended_at"

// openStore is the cloud.NewOrgStore factory: it receives the org's already-open
// *sql.DB and installs the schema. Idempotent (IF NOT EXISTS), so reopen is a
// no-op.
func openStore(db *sql.DB) (*Store, error) {
	const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  project      TEXT NOT NULL DEFAULT '',
  usr          TEXT NOT NULL DEFAULT '',
  agent        TEXT NOT NULL,
  model        TEXT NOT NULL DEFAULT '',
  host         TEXT NOT NULL DEFAULT '',
  cwd          TEXT NOT NULL DEFAULT '',
  terminal_url TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'starting',
  started_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  ended_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS sessions_org_status  ON sessions(org, status);
CREATE INDEX IF NOT EXISTS sessions_org_project ON sessions(org, project);

CREATE TABLE IF NOT EXISTS session_events (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  org        TEXT NOT NULL,
  kind       TEXT NOT NULL,
  message    TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS session_events_sid ON session_events(session_id, created_at);
`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("session schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Create inserts a new session row.
func (s *Store) Create(ctx context.Context, x Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (`+sessionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.ID, x.Org, x.Project, x.User, x.Agent, x.Model, x.Host, x.Cwd,
		x.TerminalURL, x.Status, x.StartedAt, x.UpdatedAt, x.EndedAt)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// Get returns one org-scoped session by id, or errNotFound.
func (s *Store) Get(ctx context.Context, org, id string) (Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE org=? AND id=?`, org, id)
	x, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, errNotFound
	}
	return x, err
}

// List returns the org's sessions, newest first, narrowed by f. The org
// predicate leads every query; f only ANDs further, so a filter can never widen
// past the tenant boundary.
func (s *Store) List(ctx context.Context, org string, f Filter) ([]Session, error) {
	q := `SELECT ` + sessionCols + ` FROM sessions WHERE org=?`
	args := []any{org}
	if f.Live {
		q += ` AND ended_at=0`
	}
	if f.Status != "" {
		q += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.Project != "" {
		q += ` AND project=?`
		args = append(args, f.Project)
	}
	if f.Host != "" {
		q += ` AND host=?`
		args = append(args, f.Host)
	}
	if f.Agent != "" {
		q += ` AND agent=?`
		args = append(args, f.Agent)
	}
	q += ` ORDER BY started_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Patch is the mutable subset of a session. A nil field leaves the column
// unchanged, so a heartbeat that only refreshes updated_at need not resend the
// terminal URL.
type Patch struct {
	Status      *string
	TerminalURL *string
	Model       *string
	EndedAt     *int64
}

// Update applies p to one org-scoped session and returns the new row. updated_at
// is always refreshed to now. Returns errNotFound when the id is absent.
func (s *Store) Update(ctx context.Context, org, id string, now int64, p Patch) (Session, error) {
	sets := []string{"updated_at=?"}
	args := []any{now}
	if p.Status != nil {
		sets = append(sets, "status=?")
		args = append(args, *p.Status)
	}
	if p.TerminalURL != nil {
		sets = append(sets, "terminal_url=?")
		args = append(args, *p.TerminalURL)
	}
	if p.Model != nil {
		sets = append(sets, "model=?")
		args = append(args, *p.Model)
	}
	if p.EndedAt != nil {
		sets = append(sets, "ended_at=?")
		args = append(args, *p.EndedAt)
	}
	args = append(args, org, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET `+strings.Join(sets, ", ")+` WHERE org=? AND id=?`, args...)
	if err != nil {
		return Session{}, fmt.Errorf("update session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Session{}, errNotFound
	}
	return s.Get(ctx, org, id)
}

// Delete removes one org-scoped session and its events. Returns errNotFound when
// the id is absent.
func (s *Store) Delete(ctx context.Context, org, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE org=? AND id=?`, org, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM session_events WHERE org=? AND session_id=?`, org, id)
	return nil
}

// AddEvent records one lifecycle event against an existing org-scoped session.
// It first confirms the session exists in this org (so an event can never be
// filed against another tenant's id), then inserts.
func (s *Store) AddEvent(ctx context.Context, e Event) error {
	if _, err := s.Get(ctx, e.Org, e.SessionID); err != nil {
		return err // errNotFound propagates → 404
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_events (id, session_id, org, kind, message, created_at) VALUES (?,?,?,?,?,?)`,
		e.ID, e.SessionID, e.Org, e.Kind, e.Message, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// ListEvents returns a session's events oldest-first, org-scoped.
func (s *Store) ListEvents(ctx context.Context, org, sessionID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, org, kind, message, created_at FROM session_events
		 WHERE org=? AND session_id=? ORDER BY created_at ASC`, org, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Org, &e.Kind, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scanSession reads one sessions row from a *sql.Row or *sql.Rows.
func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	err := sc.Scan(&x.ID, &x.Org, &x.Project, &x.User, &x.Agent, &x.Model, &x.Host,
		&x.Cwd, &x.TerminalURL, &x.Status, &x.StartedAt, &x.UpdatedAt, &x.EndedAt)
	return x, err
}
