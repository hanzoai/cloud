package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// A live agent-session is a running invocation — a cloud agent run, a bot loop,
// or a @hanzo/dev CLI run spawning subagents. The SUBAGENT TREE is sessions
// linked by ParentID: the outer agent is the root (ParentID==""), each spawned
// subagent is a child, and RootID is the tree key every node in one flow shares.
// It is the first-class, streamable form of the blue/red/cto fan-out tree.
//
// A session is NOT foreign-keyed to an agents row: an external surface (the
// @hanzo/dev CLI) registers a session whose Agent is just a label, not a cloud
// Agent definition. Tenancy is the org's own file, exactly like agents/runs.
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

	// Execution context — WHERE this session runs. All optional (a surface that
	// doesn't know sets ""), surfaced by mission-control so a card shows the
	// machine/repo/cwd it runs on and the devices view maps "which sessions run
	// where". Host is the machine label; Repo/Cwd are the code context; Target is a
	// registered run-target id (the #48 dispatch association — resolved same-org at
	// register/patch so it never points across tenants). Truth the SURFACE reports.
	Host   string
	Cwd    string
	Repo   string
	Target string

	// Terminal is where this session's live terminal can be WATCHED — the URL the
	// machine published for it (zrok gives one without opening a port). Optional:
	// a session that publishes nothing is still a session, it just cannot be
	// watched. It is a URL rather than a stream because the bytes belong to the
	// machine running the shell; cloud holds the address, never the connection.
	Terminal string

	// Provider/Account tag a session with the linked AI account it ran under (the
	// login-manager tie-in): which provider (claude|codex|hanzo|…) and which
	// subscription/api account served this run. Optional (a surface that doesn't
	// know sets ""), surfaced so the cockpit shows which linked account served
	// the run and so a login-out (link revoke) can stop the sessions that used it.
	Provider string
	Account  string

	// Project / Published are the READABLE BUILD (provenance.go): which product
	// this session built, and its author's decision to let the world read the
	// story. Two columns, because "the build of project P" is just the sessions
	// tagged with P — a build log is not a second kind of thing to store.
	// Published only ever widens READ access to a session that already exists;
	// it grants nothing else, and an unpublished session is invisible to the
	// public route no matter who asks.
	Project   string
	Published bool

	// Room is the collaborative room this session was started IN (HIP-0523) — the
	// room a person mentioned an agent in, so a space view can ask "what has
	// been run in #bugfix-1010" and a run can be read back to the conversation that
	// asked for it.
	//
	// It is a LABEL the starting surface reports, exactly as Repo and Host are,
	// and it is deliberately not resolved: the channel lives in another app's
	// per-space document store, and a session that outlives its channel
	// should keep saying where it came from rather than losing its provenance to
	// a dangling reference. Empty means the run has no room — a CLI session, a
	// schedule, an API call — which is most of them.
	Room string

	// Payer is WHOSE BOOKS pay for this session's runtime — the wallet
	// cloud.PayerOf resolved for the act that opened it. Not the org: a platform
	// SuperAdmin working inside somebody else's org spends their own.
	//
	// A session opened before the runtime meter carries none, and the meter reads
	// the org's own pool for those rather than guessing a person.
	Payer string
	// MeteredAt is the second through which this session's runtime has been
	// billed — cloud.Running.MeteredAt. Zero means the meter has never seen it,
	// which starts the clock at now and charges nothing for the time before.
	MeteredAt int64

	// The last ESTIMATE of how far this run has got (progress.go): the share of
	// its goal that is done, what shape it is in, the one line saying what it is
	// doing, and when a model last read the transcript to say so.
	//
	// ProgressPct is pctUnknown (-1) for a run whose progress is indeterminate,
	// which is a DIFFERENT fact from 0 and must never render as one; ProgressAt is
	// zero until something has estimated it, and doubles as the debounce clock, so
	// nothing else may write it.
	//
	// ProgressEstimated says a MODEL produced the value rather than the run
	// itself. A run REPORTS its own progress by appending a `progress` turn
	// (progress.go), which is ground truth and outranks a guess; the estimator
	// only ever writes where a self-report has not, so the two never contend for
	// one row.
	ProgressPct       int
	ProgressPhase     string
	ProgressActivity  string
	ProgressAt        int64
	ProgressEstimated bool
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
// one org's file carries its agents, runs, sessions and events — one store, one
// handle, one transaction domain. Idempotent (IF NOT EXISTS).
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
  task_run_id      TEXT NOT NULL DEFAULT '',
  host             TEXT NOT NULL DEFAULT '',
  cwd              TEXT NOT NULL DEFAULT '',
  repo             TEXT NOT NULL DEFAULT '',
  target           TEXT NOT NULL DEFAULT '',
  provider         TEXT NOT NULL DEFAULT '',
  account          TEXT NOT NULL DEFAULT ''
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
	// Forward, idempotent: a sessions table created before the execution-context
	// columns existed gains them here (the CREATE above only runs on a fresh DB).
	if err := s.addColumns("agent_sessions", map[string]string{
		"host":     "TEXT NOT NULL DEFAULT ''",
		"cwd":      "TEXT NOT NULL DEFAULT ''",
		"repo":     "TEXT NOT NULL DEFAULT ''",
		"target":   "TEXT NOT NULL DEFAULT ''",
		"terminal": "TEXT NOT NULL DEFAULT ''",
		"provider": "TEXT NOT NULL DEFAULT ''",
		"account":  "TEXT NOT NULL DEFAULT ''",
		// The readable build (provenance.go). Defaults keep every pre-existing
		// session exactly as it was: untagged, and NOT published.
		"project":   "TEXT NOT NULL DEFAULT ''",
		"published": "INTEGER NOT NULL DEFAULT 0",
		// The room a run was started in. Default keeps every pre-existing session
		// exactly as it was: attributed to no room.
		"room": "TEXT NOT NULL DEFAULT ''",
		// The runtime meter. Both defaults are REFUSALS rather than values: a
		// session that predates the meter names no payer, so its runtime is
		// charged to its org's own pool, and carries no watermark, so its clock
		// starts at the first tick that sees it and it is never billed for the
		// hours it ran before anybody was counting.
		"payer":      "TEXT NOT NULL DEFAULT ''",
		"metered_at": "INTEGER NOT NULL DEFAULT 0",
		// The progress estimate. Every default is a REFUSAL, like the meter's two
		// above: -1 says nothing has estimated this run and its progress is
		// INDETERMINATE (0 would say it has done none of its work, which is a
		// claim about every session that predates this column), and 0 on the
		// stamp says nothing has read its transcript yet.
		"progress_pct":       "INTEGER NOT NULL DEFAULT -1",
		"progress_phase":     "TEXT NOT NULL DEFAULT ''",
		"progress_activity":  "TEXT NOT NULL DEFAULT ''",
		"progress_at":        "INTEGER NOT NULL DEFAULT 0",
		"progress_estimated": "INTEGER NOT NULL DEFAULT 1",
	}); err != nil {
		return err
	}
	// Indexes on the execution-context columns are created AFTER addColumns: on an
	// upgrade over a prior-release table these columns don't exist until addColumns
	// runs, and a CREATE INDEX in the table DDL above would reference a not-yet-added
	// column and fail on the old schema ("no such column: target").
	if _, err := s.db.Exec(`
CREATE INDEX IF NOT EXISTS ix_sessions_org_target ON agent_sessions(org, target);
CREATE INDEX IF NOT EXISTS ix_sessions_org_host ON agent_sessions(org, host);
CREATE INDEX IF NOT EXISTS ix_sessions_org_account ON agent_sessions(org, provider, account);
CREATE INDEX IF NOT EXISTS ix_sessions_org_project ON agent_sessions(org, project, created_at);
CREATE INDEX IF NOT EXISTS ix_sessions_published ON agent_sessions(published, updated_at);
CREATE INDEX IF NOT EXISTS ix_sessions_org_room ON agent_sessions(org, room, created_at);
CREATE INDEX IF NOT EXISTS ix_sessions_meter ON agent_sessions(org, ended_at, metered_at);`); err != nil {
		return fmt.Errorf("migrate sessions indexes: %w", err)
	}
	return nil
}

// eventCols is the event projection, named ONCE for the same reason sessionCols
// is: four statements read or write it and a fifth (the legacy fan-out) copies it.
const eventCols = `id,session_id,org,seq,kind,actor,payload,created_at`

const sessionCols = `id,org,agent,actor,status,parent_id,root_id,title,started_at,ended_at,created_at,updated_at,task_workflow_id,task_run_id,host,cwd,repo,terminal,target,provider,account,project,published,room,payer,metered_at,progress_pct,progress_phase,progress_activity,progress_at,progress_estimated`

// sessionVals is sessionCols' placeholder list, DERIVED from it rather than
// written out beside it. Every INSERT over a named column list now does the same
// — see legacy.go, where four more were spelled by hand until the runtime meter's
// two columns made three of them fail with `14 values for 16 columns`, which is
// this comment's own prediction arriving on schedule. Two INSERTs use it — the ordinary create and the legacy
// fan-out — and both used to spell the run of `?` by hand against a column list
// that is named once. That is a column count in three places, and adding the
// twenty-fourth column proved it: the create was updated, the fan-out was not,
// and the failure was `23 values for 24 columns` at RUN time in a migration path
// that only executes on an upgrade over a pre-split database. Derived, the
// arithmetic cannot disagree with the columns.
var sessionVals = placeholders(sessionCols)

// placeholders renders one `?` per comma-separated column in cols.
func placeholders(cols string) string {
	return strings.TrimSuffix(strings.Repeat("?,", strings.Count(cols, ",")+1), ",")
}

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	err := sc.Scan(&x.ID, &x.Org, &x.Agent, &x.Actor, &x.Status, &x.ParentID, &x.RootID,
		&x.Title, &x.StartedAt, &x.EndedAt, &x.CreatedAt, &x.UpdatedAt,
		&x.TaskWorkflowID, &x.TaskRunID, &x.Host, &x.Cwd, &x.Repo, &x.Terminal, &x.Target,
		&x.Provider, &x.Account, &x.Project, &x.Published, &x.Room, &x.Payer, &x.MeteredAt,
		&x.ProgressPct, &x.ProgressPhase, &x.ProgressActivity, &x.ProgressAt, &x.ProgressEstimated)
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
	// A session is born with INDETERMINATE progress, and the store settles that
	// rather than trusting the caller to: Go's zero value for ProgressPct is 0,
	// which on this column means "none of the work is done" — a claim about a run
	// that has not started. Writing it here is what makes "unknown never renders
	// as zero" hold by construction instead of by every register path remembering.
	if x.ProgressPhase == "" && x.ProgressAt == 0 {
		x.ProgressPct = pctUnknown
		// Nothing has estimated it and nothing has reported it, so neither word is
		// the row's. progressOf reads the phase first and answers "unknown"
		// regardless; this keeps the column from asserting a provenance for a
		// value that does not exist.
		x.ProgressEstimated = false
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_sessions (`+sessionCols+`) VALUES (`+sessionVals+`)`,
		x.ID, x.Org, x.Agent, x.Actor, x.Status, x.ParentID, x.RootID, x.Title,
		x.StartedAt, x.EndedAt, x.CreatedAt, x.UpdatedAt, x.TaskWorkflowID, x.TaskRunID,
		x.Host, x.Cwd, x.Repo, x.Terminal, x.Target, x.Provider, x.Account, x.Project, x.Published,
		x.Room, x.Payer, x.MeteredAt,
		x.ProgressPct, x.ProgressPhase, x.ProgressActivity, x.ProgressAt, x.ProgressEstimated)
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
//
// Project narrows to the sessions that built one product; it is orthogonal to
// the structural axis, so `?project=x` alone lists that build's ROOTS (the
// default parent_id==” scope) and pairs with Root to walk its subagents.
// Published additionally requires the author's publish flag — the predicate the
// PUBLIC build route runs, so an unpublished session cannot be reached anonymously.
type SessionFilter struct {
	Root      string
	Parent    string
	Status    string
	Project   string
	Published bool
	// Room narrows to the sessions started in one collaborative room, which is what
	// makes "the runs of #bugfix-1010" a query rather than a scan.
	Room string
	// Actor narrows the list to the sessions one principal opened. A session is
	// a person's conversation even inside a shared org, so the door sets this to
	// the caller for everyone who is not an org admin; empty lists the org.
	Actor string
	Limit int
}

// ListSessions returns an org's sessions per filter, newest first, capped.
func (s *Store) ListSessions(ctx context.Context, org string, f SessionFilter) ([]Session, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := "org=?"
	args := []any{org}
	if f.Actor != "" {
		where += " AND (actor=? OR actor='')"
		args = append(args, f.Actor)
	}
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
	if f.Project != "" {
		where += " AND project=?"
		args = append(args, f.Project)
	}
	if f.Room != "" {
		where += " AND room=?"
		args = append(args, f.Room)
	}
	if f.Published {
		where += " AND published=1"
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

// UpdateSession persists the mutable fields of an existing (org,id) session.
// Scoped by org so a cross-tenant id can never mutate another's session.
//
// Every column the patch can set has to be listed here. A field added to
// patchSessionIn and forgotten in this statement accepts the request, answers
// 200 with the new value in the response body, and persists nothing — a success
// that did nothing, which is the hardest shape of bug to see.
func (s *Store) UpdateSession(ctx context.Context, x Session) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=?, title=?, ended_at=?, updated_at=?, target=?,
		        terminal=?, project=?, published=?, cwd=?
		 WHERE org=? AND id=?`,
		x.Status, x.Title, x.EndedAt, x.UpdatedAt, x.Target, x.Terminal, x.Project, x.Published,
		x.Cwd, x.Org, x.ID)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errSessionNotFound
	}
	return nil
}

// ListPublishedBuilds returns every published build ACROSS ORGS, newest first.
// It is the one query in this file that is deliberately not org-scoped, because
// it answers a public question — "which builds may anyone read?" — and its WHERE
// clause is the publish flag itself. A row can only appear here because its own
// author set published=1, so cross-org visibility is the author's grant, not a
// missing tenant predicate. Roots only: a build's story is its outer session.
func (s *Store) ListPublishedBuilds(ctx context.Context, limit int) ([]Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions
		 WHERE published=1 AND project<>'' AND parent_id=''
		 ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list published builds: %w", err)
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

// AppendEvent inserts one event, allocating the next per-session Seq. The org's
// file runs on a single connection (cloud.OrgDB sets MaxOpenConns(1)) so the
// read-then-write of the max seq is serialised WITHIN the org, and the
// UNIQUE(session_id,seq) index is the final backstop. Appends in different orgs
// no longer queue behind each other, because they are different files.
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
		`INSERT INTO agent_session_events (`+eventCols+`) VALUES (?,?,?,?,?,?,?,?)`,
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
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=? AND session_id=? AND seq>?
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

// TailEvents returns a session's most recent events, OLDEST of those first, so a
// reader gets a transcript to read down rather than a reversed one.
//
// It is not ListEvents with a bigger limit, and the difference is the one that
// bites: ListEvents pages FORWARD from a cursor, so asking it for 20 with no
// cursor answers with a run's first twenty turns — the opposite end of the log
// from the one "what is it doing NOW" is asked about. The DESC scan rides the
// same (session_id, seq) index the ASC one does.
func (s *Store) TailEvents(ctx context.Context, org, sessionID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=? AND session_id=?
		 ORDER BY seq DESC LIMIT ?`, org, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("tail events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Org, &e.Seq, &e.Kind, &e.Actor,
			&e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tail event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
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

// LastEvent returns a session's most recent event (highest seq) — the one-line
// "last activity" a mission-control card shows in the list without fetching full
// detail. ok=false when the session has no events yet. Org-scoped like every read.
func (s *Store) LastEvent(ctx context.Context, org, sessionID string) (Event, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=? AND session_id=?
		 ORDER BY seq DESC LIMIT 1`, org, sessionID)
	var e Event
	err := row.Scan(&e.ID, &e.SessionID, &e.Org, &e.Seq, &e.Kind, &e.Actor, &e.Payload, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, fmt.Errorf("last event: %w", err)
	}
	return e, true, nil
}

// AdvanceSession moves one session's runtime watermark from was to now and reports
// whether THIS caller moved it. See Store.Advance for why the compare is the whole
// of it, and cloud.RuntimeCharge for the order it is called in.
//
// UpdateSession deliberately does not touch this column: it writes a struct read
// at the top of a handler, and a tick landing in between would be undone.
func (s *Store) AdvanceSession(ctx context.Context, org, id string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET metered_at=? WHERE org=? AND id=? AND metered_at=?`,
		now, org, id, was)
	if err != nil {
		return false, fmt.Errorf("advance session meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetProgress writes one session's estimate and the instant it was made.
//
// It is a targeted UPDATE for exactly the reason AdvanceSession is: UpdateSession
// writes a struct read at the top of a handler, so an estimate landing between
// that read and that write would be silently undone — and this one is written by
// a goroutine that runs beside every read, which is precisely when that race is
// most likely rather than least.
//
// progress_at is the debounce clock as well as the stamp, so this is the ONLY
// writer of it. A caller that could move the estimate without moving the clock,
// or the clock without the estimate, would have two facts where there is one.
func (s *Store) SetProgress(ctx context.Context, org, id string, p Progress) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET progress_pct=?, progress_phase=?, progress_activity=?,
		        progress_at=?, progress_estimated=?
		 WHERE org=? AND id=?`,
		p.Pct, p.Phase, p.Activity, p.At, p.Estimated, org, id)
	if err != nil {
		return fmt.Errorf("set progress: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errSessionNotFound
	}
	return nil
}

// SetEstimate writes a MODEL estimate, and only if nothing has written progress
// since `was`. It reports whether THIS caller wrote — the same compare-and-set
// shape AdvanceSession uses on the meter's watermark, for the same reason.
//
// The unconditional SetProgress above is the RUN's own word, which always wins at
// the instant it is written. An estimate is formed from a transcript read seconds
// earlier, so it may arrive after a self-report that supersedes it; the compare is
// what makes "a fresh self-report outranks a guess" true of the RACE and not only
// of the read. A caller that loses keeps its own answer and writes nothing, which
// is correct twice over — the newer word is better, and the stamp it did not move
// is already newer than the interval.
//
// It writes p's OWN provenance rather than a literal 1, because the estimator
// also calls it to KEEP a value it could not replace: an outage advances the
// clock over whatever was there, and stamping "estimated" would re-label the
// run's own report as a guess the first time the gateway hiccuped. The column
// says who produced the VALUE, and this write produces none.
func (s *Store) SetEstimate(ctx context.Context, org, id string, p Progress, was int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET progress_pct=?, progress_phase=?, progress_activity=?,
		        progress_at=?, progress_estimated=?
		 WHERE org=? AND id=? AND progress_at=?`,
		p.Pct, p.Phase, p.Activity, p.At, p.Estimated, org, id, was)
	if err != nil {
		return false, fmt.Errorf("set estimate: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Live is every session of this org that has not ended: the set the reaper reads.
//
// ONE FACT AND NO POLICY. `ended_at = 0` is the row's own statement that it has not
// ended — the same predicate Unbilled below uses for the same word — and nothing
// else is asked here. Whether one of them is OVER is clocks.over's question
// (reap.go), answered in Go over the stamps this returns; the status is one of its
// terms and so belongs there. That split is apps/sandbox's, in as many words: an
// expiry half in a WHERE clause and half in a loop is one rule living in two places
// that cannot see each other.
//
// Adding `status IN (running, paused)` beside it looks like defence and is not: a
// terminal row always carries an end, so the two clauses select the same rows, and
// a second spelling of one fact is only somewhere for the two to disagree later.
//
// NO LIMIT, for the reason Unbilled has none — the set IS the org's live sessions,
// which is the quantity the pass reading it exists to bound.
func (s *Store) Live(ctx context.Context, org string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE org=? AND ended_at=0`, org)
	if err != nil {
		return nil, fmt.Errorf("list live sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan live session: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Unbilled is every session of this org that still owes runtime, with NO LIMIT.
//
// TWO SHAPES, ONE QUERY, and the second is why a close needs no code of its own:
//
//	ended_at = 0                          still open — bills up to now, every tick
//	metered_at > 0 AND ended_at > metered_at   ended, and its tail is unbilled
//
// A session billed through its own end satisfies neither and leaves the set for
// good, so the set shrinks to the open sessions plus whatever closed since the
// last tick. That is what lets the close path be nothing at all: a session that
// ends is picked up by the next sweep, charged [watermark, ended], and drops out —
// rather than four writers of ended_at each having to remember to bill.
//
// The `metered_at > 0` on the second shape excludes sessions that ENDED before
// this meter existed, so shipping it does not walk an org's whole history.
func (s *Store) Unbilled(ctx context.Context, org string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions
		 WHERE org=? AND (ended_at=0 OR (metered_at>0 AND ended_at>metered_at))`, org)
	if err != nil {
		return nil, fmt.Errorf("list unbilled sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unbilled session: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
