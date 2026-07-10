package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A live agent-session is a running invocation — a cloud agent run, a bot loop,
// or a @hanzo/dev CLI run spawning subagents. The SUBAGENT TREE is sessions
// linked by ParentID: the outer agent is the root (ParentID==""), each spawned
// subagent is a child, and RootID is the tree key every node in one flow shares.
// It is the first-class, streamable form of the blue/red/cto fan-out tree.
//
// A session is NOT foreign-keyed to an agents row: an external surface (the
// @hanzo/dev CLI) registers a session whose Agent is just a label, not a cloud
// Agent definition. Tenant isolation is the Org column, enforced on every query
// exactly like agents/runs — one file (agents.db), tenancy is the org.
type Session struct {
	ID        string
	Org       string
	Agent     string // agent name / type label (need not be a cloud Agent row)
	Actor     string // the principal that started it (validated user, or a bound SA)
	Status    string // running|paused|done|error
	ParentID  string // "" for a root (the outer agent)
	RootID    string // the tree key; == ID for a root
	Title     string
	StartedAt int64
	EndedAt   int64 // 0 until a terminal status is reached
	CreatedAt int64
	UpdatedAt int64

	// TaskWorkflowID / TaskRunID link this session to the hanzoai/tasks durable
	// workflow that actually EXECUTES it. This registry is the view/control/stream
	// layer; durable execution (retries, resumability, scheduling) is owned by
	// hanzoai/tasks — NOT by a bespoke scheduler here. A root session maps to a
	// tasks workflow (ExecuteWorkflow); a subagent maps to a child workflow keyed
	// by the same RootID. When these are set, control (pause/resume/stop/message)
	// forwards to the tasks Signal/Cancel API (see State.tasks). Empty = a surface
	// that consumes control from the event stream instead (today's @hanzo/dev).
	TaskWorkflowID string
	TaskRunID      string
}

// Event is one entry in a session's ordered log: a model message, a tool call, a
// subagent spawn, a free log line, a status change, or a control command the
// running surface consumes. Seq is monotonic PER SESSION so a subscriber can
// resume from its last-seen point; Org is denormalised so every read stays
// org-scoped without a join back to the session row.
type Event struct {
	ID        string
	SessionID string
	Org       string
	Seq       int64
	Kind      string // message|tool-call|spawn|log|status|control
	Actor     string
	Payload   string // opaque JSON blob (validated well-formed, size-bounded)
	CreatedAt int64
}

// Session status values. running/paused are live; done/error are terminal.
const (
	StatusRunning = "running"
	StatusPaused  = "paused"
	StatusDone    = "done"
	StatusError   = "error"
)

func isTerminalStatus(s string) bool { return s == StatusDone || s == StatusError }

func validStatus(s string) bool {
	switch s {
	case StatusRunning, StatusPaused, StatusDone, StatusError:
		return true
	}
	return false
}

var (
	errSessionNotFound = errors.New("agents: session not found")
	errParentNotFound  = errors.New("agents: parent session not found")
)

// migrateSessions creates the session + event tables. Called from migrate() so
// the ONE agents.db carries agents, runs, sessions and events — one store, one
// tenancy column, no second DB handle. Idempotent (IF NOT EXISTS).
func (s *Store) migrateSessions() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agent_sessions (
  id               TEXT PRIMARY KEY,
  org              TEXT NOT NULL,
  agent            TEXT NOT NULL DEFAULT '',
  actor            TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'running',
  parent_id        TEXT NOT NULL DEFAULT '',
  root_id          TEXT NOT NULL DEFAULT '',
  title            TEXT NOT NULL DEFAULT '',
  started_at       INTEGER NOT NULL,
  ended_at         INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  task_workflow_id TEXT NOT NULL DEFAULT '',
  task_run_id      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_sessions_org_root ON agent_sessions(org, root_id, created_at);
CREATE INDEX IF NOT EXISTS ix_sessions_org_parent ON agent_sessions(org, parent_id, created_at);
CREATE INDEX IF NOT EXISTS ix_sessions_org_status ON agent_sessions(org, status, updated_at);

CREATE TABLE IF NOT EXISTS agent_session_events (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  org        TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  payload    TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_events_session_seq ON agent_session_events(session_id, seq);
CREATE INDEX IF NOT EXISTS ix_events_org_session_seq ON agent_session_events(org, session_id, seq);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate sessions: %w", err)
	}
	return nil
}

const sessionCols = `id,org,agent,actor,status,parent_id,root_id,title,started_at,ended_at,created_at,updated_at,task_workflow_id,task_run_id`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	err := sc.Scan(&x.ID, &x.Org, &x.Agent, &x.Actor, &x.Status, &x.ParentID, &x.RootID,
		&x.Title, &x.StartedAt, &x.EndedAt, &x.CreatedAt, &x.UpdatedAt,
		&x.TaskWorkflowID, &x.TaskRunID)
	return x, err
}

// CreateSession inserts one session. When ParentID is set it MUST reference an
// existing session IN THE SAME ORG — the caller resolves it via GetSession first
// so a cross-tenant or dangling parent can never link a tree. RootID is derived
// by the caller (parent's root, or self for a root); this method persists what it
// is given after a final same-org sanity check on the parent.
func (s *Store) CreateSession(ctx context.Context, x Session) error {
	if x.ParentID != "" {
		// Re-verify the parent under the SAME org inside the write path so a
		// TOCTOU between the handler's lookup and here cannot smuggle a foreign
		// or deleted parent into the tree (fail-closed).
		var org string
		err := s.db.QueryRowContext(ctx,
			`SELECT org FROM agent_sessions WHERE id=? AND org=?`, x.ParentID, x.Org).Scan(&org)
		if errors.Is(err, sql.ErrNoRows) {
			return errParentNotFound
		}
		if err != nil {
			return fmt.Errorf("verify parent: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_sessions (`+sessionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.ID, x.Org, x.Agent, x.Actor, x.Status, x.ParentID, x.RootID, x.Title,
		x.StartedAt, x.EndedAt, x.CreatedAt, x.UpdatedAt, x.TaskWorkflowID, x.TaskRunID)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// GetSession returns the (org,id) session or errSessionNotFound. The org is part
// of the key so one tenant can never resolve another's session id.
func (s *Store) GetSession(ctx context.Context, org, id string) (Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE org=? AND id=?`, org, id)
	x, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, errSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	return x, nil
}

// SessionFilter selects a slice of an org's sessions. The fields are AND-ed; a
// zero field is "any". Scope picks the structural axis:
//   - Root set   -> every session in that tree (root_id == Root).
//   - Parent set -> the direct children of Parent (parent_id == Parent).
//   - neither    -> roots only (parent_id == ”), the outer-agent view.
type SessionFilter struct {
	Root   string
	Parent string
	Status string
	Limit  int
}

// ListSessions returns an org's sessions per filter, newest first, capped.
func (s *Store) ListSessions(ctx context.Context, org string, f SessionFilter) ([]Session, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := "org=?"
	args := []any{org}
	switch {
	case f.Root != "":
		where += " AND root_id=?"
		args = append(args, f.Root)
	case f.Parent != "":
		where += " AND parent_id=?"
		args = append(args, f.Parent)
	default:
		where += " AND parent_id=''"
	}
	if f.Status != "" {
		where += " AND status=?"
		args = append(args, f.Status)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE `+where+
			` ORDER BY created_at DESC, id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ListTree returns EVERY session in one org's tree (root_id == root), oldest
// first so a caller can assemble parent→child in a single pass. Capped so a
// pathological tree can't produce an unbounded response.
func (s *Store) ListTree(ctx context.Context, org, root string, cap int) ([]Session, error) {
	if cap <= 0 || cap > 10000 {
		cap = 10000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE org=? AND root_id=?
		 ORDER BY created_at ASC, id ASC LIMIT ?`, org, root, cap)
	if err != nil {
		return nil, fmt.Errorf("list tree: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// UpdateSession persists status/title/ended_at for an existing (org,id) session.
// Scoped by org so a cross-tenant id can never mutate another's session.
func (s *Store) UpdateSession(ctx context.Context, x Session) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=?, title=?, ended_at=?, updated_at=?
		 WHERE org=? AND id=?`,
		x.Status, x.Title, x.EndedAt, x.UpdatedAt, x.Org, x.ID)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errSessionNotFound
	}
	return nil
}

// CountChildren returns how many DIRECT children a session has (its fan-out).
func (s *Store) CountChildren(ctx context.Context, org, id string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_sessions WHERE org=? AND parent_id=?`, org, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count children: %w", err)
	}
	return n, nil
}

// AppendEvent inserts one event, allocating the next per-session Seq. The store
// runs on a single connection (SetMaxOpenConns(1)) so the read-then-write of the
// max seq is serialised; the UNIQUE(session_id,seq) index is the final backstop.
// The session's updated_at is bumped in the SAME transaction so "last activity"
// stays truthful. Returns the persisted event (with Seq/CreatedAt) for streaming.
func (s *Store) AppendEvent(ctx context.Context, e Event) (Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var next int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM agent_session_events WHERE session_id=?`,
		e.SessionID).Scan(&next); err != nil {
		return Event{}, fmt.Errorf("next seq: %w", err)
	}
	e.Seq = next
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_session_events (id,session_id,org,seq,kind,actor,payload,created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		e.ID, e.SessionID, e.Org, e.Seq, e.Kind, e.Actor, e.Payload, e.CreatedAt); err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_sessions SET updated_at=? WHERE org=? AND id=?`,
		e.CreatedAt, e.Org, e.SessionID); err != nil {
		return Event{}, fmt.Errorf("bump session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit: %w", err)
	}
	return e, nil
}

// ListEvents returns a session's events in Seq order (optionally only those with
// Seq > since, so a subscriber resumes exactly where it dropped), capped.
func (s *Store) ListEvents(ctx context.Context, org, sessionID string, since int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,session_id,org,seq,kind,actor,payload,created_at
		 FROM agent_session_events WHERE org=? AND session_id=? AND seq>?
		 ORDER BY seq ASC LIMIT ?`, org, sessionID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Org, &e.Seq, &e.Kind, &e.Actor,
			&e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventCountsByRoot returns per-session event counts for EVERY session in one
// org's tree (root_id == root) in a SINGLE grouped query — so materialising a
// tree of N nodes with real per-node event counts costs one round trip, not N,
// and never hits SQLite's bound-parameter limit (the join scopes by root_id, not
// an IN list of ids).
func (s *Store) EventCountsByRoot(ctx context.Context, org, root string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.session_id, COUNT(*)
		 FROM agent_session_events e
		 JOIN agent_sessions s ON s.id = e.session_id AND s.org = e.org
		 WHERE e.org=? AND s.root_id=?
		 GROUP BY e.session_id`, org, root)
	if err != nil {
		return nil, fmt.Errorf("event counts by root: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// CountEvents returns how many events a session has (the list rollup).
func (s *Store) CountEvents(ctx context.Context, org, sessionID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_session_events WHERE org=? AND session_id=?`,
		org, sessionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return n, nil
}
