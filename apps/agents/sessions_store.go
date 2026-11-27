package agents

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/fault"
	"github.com/hanzoai/cloud/sdk/text"
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
	Status    string // running|paused|done|error|stopped
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

	// Channel is the collaborative channel this session was started IN (HIP-0523) — the
	// channel a person mentioned an agent in, so a space view can ask "what has
	// been run in #bugfix-1010" and a run can be read back to the conversation that
	// asked for it.
	//
	// It is a LABEL the starting surface reports, exactly as Repo and Host are,
	// and it is deliberately not resolved: the channel lives in another app's
	// per-space document store, and a session that outlives its channel
	// should keep saying where it came from rather than losing its provenance to
	// a dangling reference. Empty means the run has no room — a CLI session, a
	// schedule, an API call — which is most of them.
	Channel string

	// Run is the run this session is the live view of, when it is one. Such a
	// session is billed through its run — the per-run fee and the tokens — and
	// never again for its runtime, and it cannot outlive the process executing
	// it. Empty is every other session.
	Run string

	// Box is the sandbox the session's run works in, recorded when the run leases
	// it: a turn's (box.go) or a coding run's (TagSession), and the view's
	// `sandbox` either way. A turn that is stopped or paused PARKS it rather than
	// giving it back, and a resume of the session hands it to the run that picks
	// up, so the model finds the tree it left. Empty is a run that never needed one.
	Box string

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

	// The session's own budget, integer micro-USD. Zero means none was set;
	// BudgetRemoved records a one-way removal, after which none can be set again.
	BudgetMicroUSD   int64
	BudgetRemoved    bool
	ConsumedMicroUSD int64

	// What run this session is the view of, told by the run as each becomes true
	// (TagSession): its Kind ("coding"), the Base it started from and the Branch it
	// pushes to beside Repo above, the Environment it executes in ("sandbox", or
	// the id of the org's machine it was routed to), the PR it proposed, and the
	// Mode it ran in ("build", or "plan", which writes nothing). All empty for a
	// session that is not a run's.
	Kind        string
	Base        string
	Branch      string
	Environment string
	PR          string
	Mode        string

	// Error explains why the session ended in error, when it did. Empty for any other status.
	Error string

	// Park is why a coding run is waiting to go on by itself: ParkLimit (the model
	// API refused it for a usage limit), ParkPayment (the payer refused it) or
	// ParkFault (the model API failed it, and was tried again by the harness first) or
	// ParkRestart (the pod driving it was replaced; it goes on at once on the next). A
	// parked session is PAUSED with its end written, so no sweep, meter or reaper
	// counts it live, and it holds no sandbox (Box is empty): its work is on its
	// branch. ParkAt is when it will be tried again, unix seconds, 0 when nothing
	// names a time and only money, a plan or a cap changing (or a person) goes on.
	// Empty is every other session.
	Park   string
	ParkAt int64
}

// The reasons a session is parked.
const (
	ParkLimit   = "limit"
	ParkPayment = "payment"
	ParkFault   = "fault"
	ParkRestart = "restart"
)

// Parked reports a session waiting to go on by itself.
func (x Session) Parked() bool { return x.Status == StatusPaused && x.Park != "" }

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

// Session status values. running/paused are live; done/error/stopped are
// terminal. Stopped is its own terminal because "somebody ended it" and "it
// broke" are different facts, and a stop recorded as an error reads as a fault.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusPaused  = "paused"
	StatusDone    = "done"
	StatusError   = "error"
	StatusStopped = "stopped"
)

func isTerminalStatus(s string) bool {
	return s == StatusDone || s == StatusError || s == StatusStopped
}

func validStatus(s string) bool {
	switch s {
	case StatusQueued, StatusRunning, StatusPaused, StatusDone, StatusError, StatusStopped:
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
func (s *Store) migrateSessions(ctx context.Context, tx *sql.Tx) error {
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
  started_at       BIGINT NOT NULL,
  ended_at         BIGINT NOT NULL DEFAULT 0,
  created_at       BIGINT NOT NULL,
  updated_at       BIGINT NOT NULL,
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
  seq        BIGINT NOT NULL,
  kind       TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  payload    TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_events_session_seq ON agent_session_events(session_id, seq);
CREATE INDEX IF NOT EXISTS ix_events_org_session_seq ON agent_session_events(org, session_id, seq);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate sessions: %w", err)
	}
	// Forward, idempotent: a sessions table created before the execution-context
	// columns existed gains them here (the CREATE above only runs on a fresh DB).
	if err := s.addColumns(ctx, tx, "agent_sessions", map[string]string{
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
		"published": "BIGINT NOT NULL DEFAULT 0",
		// The channel a run was started in. Default keeps every pre-existing session
		// exactly as it was: attributed to no channel. The column keeps its name (room).
		"room": "TEXT NOT NULL DEFAULT ''",
		// The run a session is the live view of. Default: none.
		"run": "TEXT NOT NULL DEFAULT ''",
		// The runtime meter. Both defaults are REFUSALS rather than values: a
		// session that predates the meter names no payer, so its runtime is
		// charged to its org's own pool, and carries no watermark, so its clock
		// starts at the first tick that sees it and it is never billed for the
		// hours it ran before anybody was counting.
		"payer":      "TEXT NOT NULL DEFAULT ''",
		"metered_at": "BIGINT NOT NULL DEFAULT 0",
		// The progress estimate. Every default is a REFUSAL, like the meter's two
		// above: -1 says nothing has estimated this run and its progress is
		// INDETERMINATE (0 would say it has done none of its work, which is a
		// claim about every session that predates this column), and 0 on the
		// stamp says nothing has read its transcript yet.
		"progress_pct":       "BIGINT NOT NULL DEFAULT -1",
		"progress_phase":     "TEXT NOT NULL DEFAULT ''",
		"progress_activity":  "TEXT NOT NULL DEFAULT ''",
		"progress_at":        "BIGINT NOT NULL DEFAULT 0",
		"progress_estimated": "BIGINT NOT NULL DEFAULT 1",
		"budget_micro_usd":   "BIGINT NOT NULL DEFAULT 0",
		"budget_removed":     "BIGINT NOT NULL DEFAULT 0",
		"consumed_micro_usd": "BIGINT NOT NULL DEFAULT 0",
		// The sandbox a run's session keeps across a stop. Default: none.
		"box": "TEXT NOT NULL DEFAULT ''",
		// What a person has said into a turn (steer.go): the last control seq its
		// run was handed, and whether the run has answered and takes no more.
		// Defaults: nothing handed over, and open.
		"steered": "BIGINT NOT NULL DEFAULT 0",
		"sealed":  "BIGINT NOT NULL DEFAULT 0",
		// What run a session is the view of (TagSession). Defaults: none — a
		// session that predates the columns is not a run's, and says nothing.
		"kind":        "TEXT NOT NULL DEFAULT ''",
		"base":        "TEXT NOT NULL DEFAULT ''",
		"branch":      "TEXT NOT NULL DEFAULT ''",
		"environment": "TEXT NOT NULL DEFAULT ''",
		"pr":          "TEXT NOT NULL DEFAULT ''",
		"mode":        "TEXT NOT NULL DEFAULT ''",
		"error":       "TEXT NOT NULL DEFAULT ''",
		// Why a coding run is parked and when it is tried again (Park). Defaults:
		// not parked.
		"park":    "TEXT NOT NULL DEFAULT ''",
		"park_at": "BIGINT NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	// Indexes on the execution-context columns are created AFTER addColumns: on an
	// upgrade over a prior-release table these columns don't exist until addColumns
	// runs, and a CREATE INDEX in the table DDL above would reference a not-yet-added
	// column and fail on the old schema (no such column: target).
	if _, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS ix_sessions_org_target ON agent_sessions(org, target);
CREATE INDEX IF NOT EXISTS ix_sessions_org_host ON agent_sessions(org, host);
CREATE INDEX IF NOT EXISTS ix_sessions_org_account ON agent_sessions(org, provider, account);
CREATE INDEX IF NOT EXISTS ix_sessions_org_project ON agent_sessions(org, project, created_at);
CREATE INDEX IF NOT EXISTS ix_sessions_published ON agent_sessions(published, updated_at);
CREATE INDEX IF NOT EXISTS ix_sessions_org_room ON agent_sessions(org, room, created_at);
CREATE INDEX IF NOT EXISTS ix_sessions_meter ON agent_sessions(org, ended_at, metered_at);
CREATE INDEX IF NOT EXISTS ix_sessions_org_kind ON agent_sessions(org, kind, created_at);`); err != nil {
		return fmt.Errorf("migrate sessions indexes: %w", err)
	}
	return s.scrubSessions(ctx, tx)
}

func (s *Store) scrubSessions(ctx context.Context, tx *sql.Tx) error {
	if s.d.Postgres() {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, org, title, error FROM agent_sessions`)
	if err != nil {
		return fmt.Errorf("read sessions for scrub: %w", err)
	}
	type patch struct {
		id, org, title, errText string
	}
	var dirty []patch
	for rows.Next() {
		var p patch
		if err := rows.Scan(&p.id, &p.org, &p.title, &p.errText); err != nil {
			_ = rows.Close()
			return err
		}
		ct := strings.ToValidUTF8(strings.ReplaceAll(p.title, "\x00", ""), "\uFFFD")
		ce := strings.ToValidUTF8(strings.ReplaceAll(p.errText, "\x00", ""), "\uFFFD")
		if ct != p.title || ce != p.errText {
			dirty = append(dirty, patch{id: p.id, org: p.org, title: ct, errText: ce})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range dirty {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET title = $1, error = $2 WHERE id = $3 AND org = $4`,
			p.title, p.errText, p.id, p.org); err != nil {
			return fmt.Errorf("scrub session %s: %w", p.id, err)
		}
	}
	return nil
}

// eventCols is the event projection, named ONCE for the same reason sessionCols
// is: four statements read or write it and a fifth (the legacy fan-out) copies it.
const eventCols = `id,session_id,org,seq,kind,actor,payload,created_at`

const sessionCols = `id,org,agent,actor,status,parent_id,root_id,title,started_at,ended_at,created_at,updated_at,task_workflow_id,task_run_id,host,cwd,repo,terminal,target,provider,account,project,published,room,run,payer,metered_at,progress_pct,progress_phase,progress_activity,progress_at,progress_estimated,budget_micro_usd,budget_removed,consumed_micro_usd,box,kind,base,branch,environment,pr,mode,error,park,park_at`

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

// End moves a live session to a terminal status and reports whether this call
// did it. The status guard makes the transition happen once: a stop and a run
// finishing can race, and whichever lands first is the answer.
//
// AN END ALREADY WRITTEN STAYS. A paused or parked session's run ended when it
// paused, and nothing ran after; moving that end to the stop would hand the
// runtime meter a span from the pause to the stop to bill.
//
// QUEUED IS LIVE TOO. It was left out, so no stop and no reap could end a queued
// row, not even at the ceiling, and the meter billed it for as long as it stood.
func (s *Store) End(ctx context.Context, org, id, status string, at int64, errStr ...string) (bool, error) {
	if !isTerminalStatus(status) {
		return false, fmt.Errorf("end session: %q is not terminal", status)
	}
	var errMsg string
	if len(errStr) > 0 {
		errMsg = errStr[0]
	}
	if status == StatusError && strings.TrimSpace(errMsg) == "" {
		errMsg = s.endedWhy(ctx, org, id)
	}
	if err := dialect.Text(errMsg); err != nil {
		return false, fmt.Errorf("end session: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=$1, ended_at=CASE WHEN ended_at=0 THEN $2 ELSE ended_at END,
		        updated_at=$3, error=$4, park='', park_at=0
		 WHERE org=$5 AND id=$6 AND status IN ($7,$8,$9)`,
		status, at, at, errMsg, org, id, StatusRunning, StatusPaused, StatusQueued)
	if err != nil {
		return false, fmt.Errorf("end session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Pause moves a running session to paused and reports whether this call did it.
// Only a running session pauses: a finished one stays finished, and a paused one
// is already the answer. Its run's end is written with it, as UpdateSession writes
// a paused one's: nothing runs in it, so the runtime meter stops at the pause.
func (s *Store) Pause(ctx context.Context, org, id string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=$1, updated_at=$2, ended_at=CASE WHEN ended_at=0 THEN $2 ELSE ended_at END
		 WHERE org=$3 AND id=$4 AND status=$5`,
		StatusPaused, at, org, id, StatusRunning)
	if err != nil {
		return false, fmt.Errorf("pause session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// EndPause writes the end of a paused session that has none, at its last
// activity, and reports whether this call did it. It is the reaper's (reap.go):
// a row paused before every pause wrote its end, or by a writer that forgot to,
// is otherwise billed as alive until the day-long ceiling. It touches nothing
// else — not the status, so it is still paused and still resumable, and not
// updated_at, because closing the span is not something the session did.
func (s *Store) EndPause(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET ended_at=CASE WHEN updated_at>started_at THEN updated_at ELSE started_at END
		 WHERE org=$1 AND id=$2 AND status=$3 AND ended_at=0`,
		org, id, StatusPaused)
	if err != nil {
		return false, fmt.Errorf("end pause: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Rest leaves a running session paused with its run over, and reports whether this
// call did it: the app's conversation between turns (talk.go). Its end is written,
// because nothing runs in it — so the sweeps that look for live sessions (Live,
// Quiet, a count of what is active) leave it alone and an org whose conversations
// all rest is settled out of them — and it is SEALED, so nothing said into it now
// is handed to a run (steer.go): what is said next is the next turn. Reopen clears
// both when that turn starts. A stop that landed first wins: only a running
// session rests, and only under run — the turn resting it — so a turn that ends
// late never rests the newer one its conversation has gone on to.
func (s *Store) Rest(ctx context.Context, org, id, run string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=$1, ended_at=$2, updated_at=$3, sealed=1 WHERE org=$4 AND id=$5 AND status=$6 AND run=$7`,
		StatusPaused, at, at, org, id, StatusRunning, run)
	if err != nil {
		return false, fmt.Errorf("rest session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rest session: %w", err)
	}
	return n > 0, nil
}

// Keep writes what an app conversation's code is — its repository, base, machine
// and mode — as its person now has it (talk.go): each one as given, so a new
// repository starts from its own default branch rather than the last one's.
func (s *Store) Keep(ctx context.Context, org, id string, w Session, at int64) error {
	if err := dialect.Text(w.Repo, w.Base, w.Environment, w.Mode); err != nil {
		return fmt.Errorf("keep session: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET repo=$1, base=$2, environment=$3, mode=$4, updated_at=$5 WHERE org=$6 AND id=$7`,
		w.Repo, w.Base, w.Environment, w.Mode, at, org, id)
	if err != nil {
		return fmt.Errorf("keep session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("keep session: %w", err)
	}
	if n == 0 {
		return errSessionNotFound
	}
	return nil
}

// Reopen puts a stopped or paused session back to running under a new run, and
// reports whether this call did it: two resumes of one session race, and the
// status guard lets exactly one of them through. It starts the session's clock
// again (reap.go measures a life from started_at), because what runs now is a new
// run and not the old one carried on — and one that takes what is said into it
// again (steer.go), until it answers.
func (s *Store) Reopen(ctx context.Context, org, id, run string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=$1, run=$2, started_at=$3, ended_at=0, updated_at=$4, sealed=0
		 WHERE org=$5 AND id=$6 AND status IN ($7,$8)`,
		StatusRunning, run, at, at, org, id, StatusStopped, StatusPaused)
	if err != nil {
		return false, fmt.Errorf("reopen session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Park leaves a coding run waiting to go on by itself, in ONE statement: it is paused,
// its end is written (the sweeps that look for live sessions, the runtime meter and the
// reaper leave it alone: nothing runs in it), it holds no sandbox, and it says why it
// waits (reason) and when it is tried again (at, unix seconds, 0 for no time). It reports
// whether this call did it. A running session parks; one already parked says why again
// (a try that was refused, or could not start); a session that ended or was stopped first
// does not.
func (s *Store) Park(ctx context.Context, org, id, reason string, at, now int64) (bool, error) {
	if err := dialect.Text(reason); err != nil {
		return false, fmt.Errorf("park session: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=$1,
		        ended_at=CASE WHEN ended_at=0 THEN $2 ELSE ended_at END,
		        metered_at=CASE WHEN box<>'' THEN (CASE WHEN ended_at=0 THEN $2 ELSE ended_at END) ELSE metered_at END,
		        updated_at=$2, park=$3, park_at=$4, box=''
		 WHERE org=$5 AND id=$6 AND (status=$7 OR (status=$1 AND park<>''))`,
		StatusPaused, now, reason, at, org, id, StatusRunning)
	if err != nil {
		return false, fmt.Errorf("park session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Unpark puts a parked session back to running, in ONE statement, and reports whether
// this call did it: two tries race, and the status guard lets exactly one through. It
// starts the session's clock and its meter again from now (reap.go measures a life from
// started_at, and the meter bills from its watermark), so the time it waited is not a
// life or a charge. payer is the wallet the run that goes on is paid from, as the person
// resolves today: the meter bills the session's stored payer, and what was stored when the
// run began may be a wallet the person is no longer paid from. An empty payer leaves it.
func (s *Store) Unpark(ctx context.Context, org, id, payer string, now int64) (bool, error) {
	if err := dialect.Text(payer); err != nil {
		return false, fmt.Errorf("unpark session: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=$1, started_at=$2, ended_at=0, updated_at=$2,
		        metered_at=CASE WHEN metered_at>0 THEN $2 ELSE metered_at END, park='', park_at=0,
		        payer=CASE WHEN $3<>'' THEN $3 ELSE payer END
		 WHERE org=$4 AND id=$5 AND status=$6 AND park<>''`,
		StatusRunning, now, payer, org, id, StatusPaused)
	if err != nil {
		return false, fmt.Errorf("unpark session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetBox records the sandbox a session's run works in.
func (s *Store) SetBox(ctx context.Context, org, id, box string) error {
	if err := dialect.Text(box); err != nil {
		return fmt.Errorf("set session box: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET box=$1 WHERE org=$2 AND id=$3`, box, org, id); err != nil {
		return fmt.Errorf("set session box: %w", err)
	}
	return nil
}

// LastSpawn is the session of the latest run session id followed (a spawn event),
// or "" when it has followed none: the conversation's latest coding run (talk.go),
// in the order the conversation took them in, which a second's clock cannot tie.
func (s *Store) LastSpawn(ctx context.Context, org, id string) (string, error) {
	var payload string
	err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM agent_session_events WHERE org=$1 AND session_id=$2 AND kind=$3 ORDER BY seq DESC LIMIT 1`,
		org, id, KindSpawn).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("last spawn: %w", err)
	}
	var p struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("last spawn: %w", err)
	}
	return p.Session, nil
}

// LastTurn is actor's latest turn in one channel — the session a "continue" said
// there means — or errSessionNotFound. Latest by activity, not by birth: a turn
// resumed and stopped again is the one somebody was last working with.
func (s *Store) LastTurn(ctx context.Context, org, actor, channel string) (Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions
		 WHERE org=$1 AND actor=$2 AND room=$3 AND run!='' ORDER BY updated_at DESC LIMIT 1`,
		org, actor, channel)
	x, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, errSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("last turn: %w", err)
	}
	return x, nil
}

// ChannelRun is the input, output and tool summary of a completed turn in one channel.
type ChannelRun struct {
	Input     string
	Output    string
	Tools     string
	StartedAt int64
}

// ChannelRuns returns completed runs in channel for org, oldest first, so a
// conversation whose transport inbox lacked the assistant's own replies can
// still reconstruct what the assistant said and which tools it called.
func (s *Store) ChannelRuns(ctx context.Context, org, channel string, limit int) ([]ChannelRun, error) {
	if channel == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.input, r.output, s.started_at,
		       COALESCE((SELECT e.payload FROM agent_session_events e WHERE e.org = s.org AND e.session_id = s.id AND e.kind = 'message' ORDER BY e.seq DESC LIMIT 1), '')
		FROM agent_sessions s
		JOIN agent_runs r ON r.org = s.org AND r.id = s.run
		WHERE s.org = $1 AND (s.room = $2 OR s.room LIKE $3) AND s.run != '' AND r.status = 'ok' AND r.output != ''
		ORDER BY s.started_at DESC, s.id DESC
		LIMIT $4`, org, channel, channel+"/%", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ChannelRun
	for rows.Next() {
		var cr ChannelRun
		var msgPayload string
		if err := rows.Scan(&cr.Input, &cr.Output, &cr.StartedAt, &msgPayload); err != nil {
			return nil, err
		}
		if msgPayload != "" {
			var m struct {
				Tools string `json:"tools"`
			}
			if json.Unmarshal([]byte(msgPayload), &m) == nil {
				cr.Tools = strings.TrimSpace(m.Tools)
			}
		}
		out = append(out, cr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// placeholders renders $1 ... $n, one per comma-separated column in cols.
func placeholders(cols string) string {
	return dialect.Marks(1, strings.Count(cols, ",")+1)
}

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	err := sc.Scan(&x.ID, &x.Org, &x.Agent, &x.Actor, &x.Status, &x.ParentID, &x.RootID,
		&x.Title, &x.StartedAt, &x.EndedAt, &x.CreatedAt, &x.UpdatedAt,
		&x.TaskWorkflowID, &x.TaskRunID, &x.Host, &x.Cwd, &x.Repo, &x.Terminal, &x.Target,
		&x.Provider, &x.Account, &x.Project, &x.Published, &x.Channel, &x.Run, &x.Payer, &x.MeteredAt,
		&x.ProgressPct, &x.ProgressPhase, &x.ProgressActivity, &x.ProgressAt, &x.ProgressEstimated,
		&x.BudgetMicroUSD, &x.BudgetRemoved, &x.ConsumedMicroUSD, &x.Box,
		&x.Kind, &x.Base, &x.Branch, &x.Environment, &x.PR, &x.Mode, &x.Error, &x.Park, &x.ParkAt)
	return x, err
}

// sessionText refuses a session whose text one engine could not store
// (dialect.Text), before a statement is written for it.
func sessionText(x Session) error {
	return dialect.Text(x.ID, x.Org, x.Agent, x.Actor, x.Status, x.ParentID, x.RootID, x.Title,
		x.TaskWorkflowID, x.TaskRunID, x.Host, x.Cwd, x.Repo, x.Terminal, x.Target, x.Provider,
		x.Account, x.Project, x.Channel, x.Run, x.Payer, x.ProgressPhase, x.ProgressActivity,
		x.Box, x.Kind, x.Base, x.Branch, x.Environment, x.PR, x.Mode, x.Error, x.Park)
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
			`SELECT org FROM agent_sessions WHERE id=$1 AND org=$2`, x.ParentID, x.Org).Scan(&org)
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
	if err := sessionText(x); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_sessions (`+sessionCols+`) VALUES (`+sessionVals+`)`,
		x.ID, x.Org, x.Agent, x.Actor, x.Status, x.ParentID, x.RootID, x.Title,
		x.StartedAt, x.EndedAt, x.CreatedAt, x.UpdatedAt, x.TaskWorkflowID, x.TaskRunID,
		x.Host, x.Cwd, x.Repo, x.Terminal, x.Target, x.Provider, x.Account, x.Project, bit(x.Published),
		x.Channel, x.Run, x.Payer, x.MeteredAt,
		x.ProgressPct, x.ProgressPhase, x.ProgressActivity, x.ProgressAt, bit(x.ProgressEstimated),
		x.BudgetMicroUSD, bit(x.BudgetRemoved), x.ConsumedMicroUSD, x.Box,
		x.Kind, x.Base, x.Branch, x.Environment, x.PR, x.Mode, x.Error, x.Park, x.ParkAt)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// GetSession returns the (org,id) session or errSessionNotFound. The org is part
// of the key so one tenant can never resolve another's session id.
func (s *Store) GetSession(ctx context.Context, org, id string) (Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE org=$1 AND id=$2`, org, id)
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
	// Channel narrows to the sessions started in one collaborative channel, which is what
	// makes "the runs of #bugfix-1010" a query rather than a scan.
	Channel string
	// Actor narrows the list to the sessions one principal opened. A session is
	// a person's conversation even inside a shared org, so the door sets this to
	// the caller for everyone who is not an org admin; empty lists the org.
	Actor string
	// Attributed leaves out the sessions recorded with no actor, which are
	// nobody's: every reader but a SuperAdmin (reader.filter).
	Attributed bool
	// Kind narrows to the sessions of one kind of run ("coding").
	Kind string
	// Private leaves out the app's conversations (talk.go) that are not Holder's:
	// a conversation is its person's, even to an admin of the org (reader.sees).
	Private bool
	Holder  string
	Limit   int
	// After resumes a page: the sessions strictly after this one in the list's
	// own order (newest first, then by id). The zero value starts at the newest.
	After Mark
}

// Mark is a position in a session list: the created_at and id of the last
// session a page carried. A position rather than an offset, so a session created
// between two reads does not shift the next page onto one already read.
type Mark struct {
	Created int64
	ID      string
}

// ListSessions returns an org's sessions per filter, newest first, capped.
func (s *Store) ListSessions(ctx context.Context, org string, f SessionFilter) ([]Session, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var b binder
	where := "org=" + b.bind(org)
	if f.Actor != "" {
		where += " AND actor=" + b.bind(f.Actor)
	}
	if f.Attributed {
		where += " AND actor<>''"
	}
	switch {
	case f.Root != "":
		where += " AND root_id=" + b.bind(f.Root)
	case f.Parent != "":
		where += " AND parent_id=" + b.bind(f.Parent)
	default:
		where += " AND parent_id=''"
	}
	if f.Status != "" {
		where += " AND status=" + b.bind(f.Status)
	}
	if f.Project != "" {
		where += " AND project=" + b.bind(f.Project)
	}
	if f.Channel != "" {
		where += " AND room=" + b.bind(f.Channel)
	}
	if f.Kind != "" {
		where += " AND kind=" + b.bind(f.Kind)
	}
	if f.Private {
		where += " AND (room<>" + b.bind(talkChannel) + " OR actor=" + b.bind(f.Holder) + ")"
	}
	if f.Published {
		where += " AND published=1"
	}
	if f.After.ID != "" {
		created, id := b.bind(f.After.Created), b.bind(f.After.ID)
		where += " AND (created_at<" + created + " OR (created_at=" + created + " AND id>" + id + "))"
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE `+where+
			` ORDER BY created_at DESC, id ASC LIMIT `+b.bind(limit), b.args...)
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

// TagSession records run facts on an (org,id) session: each non-empty field of
// t overwrites its column, and an empty one leaves the column as it was, because
// a run learns them at different moments. Scoped by org, like every write here.
//
// A SANDBOX IS HELD BY ONE LIVE SESSION AT A TIME. Naming a Box claims it: the tag
// is refused with errBoxHeld while another session that is running or paused
// holds the same box, so a follow-up never works in a sandbox another run is still
// working in — the run a steer started in it, or a second follow-up of the same
// run. The claim is one serialized check-and-write per box, so of two claims at
// once exactly one lands.
func (s *Store) TagSession(ctx context.Context, org, id string, t Session, at int64) error {
	if err := dialect.Text(t.Kind, t.Repo, t.Base, t.Branch, t.Environment, t.PR, t.Mode, t.Box, t.Project); err != nil {
		return fmt.Errorf("tag session: %w", err)
	}
	tx, err := s.d.Serialize(ctx, s.db, "box:"+org+"/"+t.Box)
	if err != nil {
		return fmt.Errorf("tag session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE agent_sessions SET
		   kind=CASE WHEN $1='' THEN kind ELSE $1 END,
		   repo=CASE WHEN $2='' THEN repo ELSE $2 END,
		   base=CASE WHEN $3='' THEN base ELSE $3 END,
		   branch=CASE WHEN $4='' THEN branch ELSE $4 END,
		   environment=CASE WHEN $5='' THEN environment ELSE $5 END,
		   pr=CASE WHEN $6='' THEN pr ELSE $6 END,
		   mode=CASE WHEN $7='' THEN mode ELSE $7 END,
		   box=CASE WHEN $8='' THEN box ELSE $8 END,
		   project=CASE WHEN $9='' THEN project ELSE $9 END,
		   updated_at=$10
		 WHERE org=$11 AND id=$12 AND ($8='' OR NOT EXISTS (
		   SELECT 1 FROM agent_sessions o WHERE o.org=$11 AND o.box=$8 AND o.id<>$12 AND o.status IN ($13,$14)))`,
		t.Kind, t.Repo, t.Base, t.Branch, t.Environment, t.PR, t.Mode, t.Box, t.Project, at, org, id,
		StatusRunning, StatusPaused)
	if err != nil {
		return fmt.Errorf("tag session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("tag session: %w", err)
	}
	if n == 0 {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM agent_sessions WHERE org=$1 AND id=$2`, org, id).Scan(&one); {
		case errors.Is(err, sql.ErrNoRows):
			return errSessionNotFound
		case err != nil:
			return fmt.Errorf("tag session: %w", err)
		}
		return errBoxHeld
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tag session: %w", err)
	}
	return nil
}

// errBoxHeld is a sandbox another live session is working in (TagSession).
var errBoxHeld = errors.New("agents: another run is still working in that sandbox; stop it or let it finish first")

// ListTree returns EVERY session in one org's tree (root_id == root), oldest
// first so a caller can assemble parent→child in a single pass. Capped so a
// pathological tree can't produce an unbounded response.
func (s *Store) ListTree(ctx context.Context, org, root string, cap int) ([]Session, error) {
	if cap <= 0 || cap > 10000 {
		cap = 10000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE org=$1 AND root_id=$2
		 ORDER BY created_at ASC, id ASC LIMIT $3`, org, root, cap)
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
//
// A PAUSE WRITES ITS END, AND A RESUME TAKES IT BACK, here rather than in each
// caller, because two callers write a status through this statement (a PATCH, and
// the budget cap pausing and resuming its run) and the rule is the store's: the
// one Rest and Park keep. Nothing runs in a paused session, so its run's end is
// written when it pauses, and the runtime meter bills it to that moment and no
// further (#768: a CLI that recorded every clean exit as paused billed each one
// for a day). The cases, decided against the row as it IS (the right-hand side
// of an UPDATE reads the old row), not the struct read at the top of a handler:
//
//	running or queued, from paused          ended_at 0; its life and its meter start again now
//	anything else, and the row has an end   it keeps it, whatever the struct says: a paused
//	                                        session closed done later ran nothing after its pause
//	paused, from a live non-paused row      the end is the pause, now
//	paused, already paused with no end      the end is its last activity (a row from before this rule)
//
// THE NUMBERS RUN IN THE ORDER THEY FIRST APPEAR, $1 to $15. The cgo SQLite binds a
// $n by its first appearance, not by its number, so a $13 written after a $15 took
// the 15th argument and the statement matched no row: every update answered
// "session not found" there, while the pure-Go engine and PostgreSQL bind by number
// and passed (internal/sqlcheck; the sdk gate TestPortedPackagesWriteSQLInTheSharedDialect).
//
// A RESUME DOES NOT FORGIVE THE TAIL. The meter may not have billed the span
// before the pause yet; moving the watermark straight to the resume would drop
// it, and leaving it would bill the pause. So the watermark moves forward by
// exactly the time the session sat paused, which keeps what it owes: (now -
// metered_at) after the resume is the running time on both sides of the pause.
// A watermark already at or past the pause (the tail was billed) moves to the
// resume. A PARKED session (one with a park reason) is Unpark's to reopen, and is
// left alone.
func (s *Store) UpdateSession(ctx context.Context, x Session) error {
	// A session in error always says why: the writer's reason, else the run's last
	// status event, else errNoReason.
	if x.Status == StatusError && strings.TrimSpace(x.Error) == "" {
		x.Error = cmp.Or(s.endedWhy(ctx, x.Org, x.ID), errNoReason)
	}
	// The first write that ends a session in error states the fault: every run that
	// ends in error is counted, with its reason, under one key per agent and reason.
	ended := false
	if x.Status == StatusError {
		before, _ := s.GetSession(ctx, x.Org, x.ID)
		ended = before.Status != StatusError
	}
	if err := dialect.Text(x.Status, x.Title, x.Target, x.Terminal, x.Project, x.Cwd, x.Error); err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET status=$1, title=$2,
		        ended_at=CASE
		          WHEN $1 IN ($3,$4) AND status=$5 AND park='' THEN 0
		          WHEN ended_at>0 THEN ended_at
		          WHEN $1=$5 AND status<>$5 THEN $6
		          WHEN $1=$5 THEN updated_at
		          ELSE $7 END,
		        started_at=CASE WHEN $1 IN ($3,$4) AND status=$5 AND park='' AND ended_at>0
		          THEN $6 ELSE started_at END,
		        metered_at=CASE WHEN $1 IN ($3,$4) AND status=$5 AND park='' AND ended_at>0 AND metered_at>0
		          THEN CASE WHEN metered_at<ended_at THEN metered_at+($6-ended_at) ELSE $6 END
		          ELSE metered_at END,
		        updated_at=$6, target=$8,
		        terminal=$9, project=$10, published=$11, cwd=$12, error=$13
		 WHERE org=$14 AND id=$15`,
		x.Status, x.Title, StatusRunning, StatusQueued, StatusPaused, x.UpdatedAt, x.EndedAt,
		x.Target, x.Terminal, x.Project, bit(x.Published), x.Cwd, x.Error, x.Org, x.ID)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errSessionNotFound
	}
	if ended && !isInterpreterScriptExit(x.Error) {
		f := fault.Ended(x.Agent, x.Error)
		f.Request = x.ID // the run's own id, so whoever started it can tell it ended
		cloud.ReportFault(f)
	}
	return nil
}

// isInterpreterScriptExit reports a normal non-zero exit from a user-supplied
// python or node program (e.g. SyntaxError, subprocess.CalledProcessError).
// Those belong to the caller's code rather than the cloud platform; signal kills
// ("was killed by …") and sandbox/lease errors still report as platform faults.
func isInterpreterScriptExit(reason string) bool {
	r := strings.TrimSpace(reason)
	return strings.HasPrefix(r, "the python harness exited ") || strings.HasPrefix(r, "the node harness exited ")
}

// statusLookback is how many of the newest status events endedWhy reads.
const statusLookback = 8

// errNoReason is what a session in error says when nothing said why.
const errNoReason = "the session ended in error and what ended it did not say why; " +
	"if it was a terminal session, install the latest CLI (curl -fsSL hanzo.sh | sh) and run it again for the reason"

// endedWhy is the error of the newest status or log event that has one, or "".
func (s *Store) endedWhy(ctx context.Context, org, id string) string {
	rows, err := s.db.QueryContext(ctx,
		`SELECT payload FROM agent_session_events WHERE org=$1 AND session_id=$2 AND kind IN ($3,$4)
		 ORDER BY seq DESC LIMIT $5`, org, id, KindStatus, KindLog, statusLookback)
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var payload string
		if rows.Scan(&payload) != nil {
			continue
		}
		var said struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &said) == nil && strings.TrimSpace(said.Error) != "" {
			return text.Cut(strings.TrimSpace(said.Error), maxError)
		}
	}
	return ""
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
		 ORDER BY updated_at DESC LIMIT $1`, limit)
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

// CountChildren returns how many DIRECT children of a session r may see (its
// fan-out, as r reads it) — the rule the listing filters by (reader.filter), so
// a count never numbers the sessions a colleague opened beneath it, nor one that
// names nobody unless a SuperAdmin asks.
func (s *Store) CountChildren(ctx context.Context, org, id string, r reader) (int, error) {
	var b binder
	q := `SELECT COUNT(*) FROM agent_sessions WHERE org=` + b.bind(org) + ` AND parent_id=` + b.bind(id)
	var f SessionFilter
	if !r.filter(&f) {
		return 0, nil
	}
	if f.Actor != "" {
		q += ` AND actor=` + b.bind(f.Actor)
	}
	if f.Attributed {
		q += ` AND actor<>''`
	}
	if f.Private {
		q += ` AND (room<>` + b.bind(talkChannel) + ` OR actor=` + b.bind(f.Holder) + `)`
	}
	var n int
	err := s.db.QueryRowContext(ctx, q, b.args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count children: %w", err)
	}
	return n, nil
}

// fanOut is a session's children as its stream frame counts them: as the
// session's own person reads them — theirs and the org's. The frame goes to
// everyone who may see the session and carries one number, so it is never one
// that numbers a colleague's sessions; an admin reads the whole fan-out from the
// session itself.
func fanOut(ctx context.Context, sto *Store, x Session) int {
	n, _ := sto.CountChildren(ctx, x.Org, x.ID, reader{actor: x.Actor})
	return n
}

// AppendEvent inserts one event, allocating the next per-session Seq. The
// read-then-write of the max seq is serialised WITHIN the org by the store's write
// lock (Dialect.BeginWrite: a file's one connection, or the schema's advisory lock),
// and the UNIQUE(session_id,seq) index is the final backstop. Appends in different
// orgs do not queue behind each other, because they are different stores.
// The session's updated_at is bumped in the SAME transaction so "last activity"
// stays truthful. Returns the persisted event (with Seq/CreatedAt) for streaming.
func (s *Store) AppendEvent(ctx context.Context, e Event) (Event, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if e, err = appendTx(ctx, tx, e); err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit: %w", err)
	}
	return e, nil
}

// appendTx is AppendEvent inside a transaction the caller holds, so a write that
// must decide something about the session and append in one step (Steer) appends
// exactly as every other write does.
//
// THE PAYLOAD IS KEPT AS PLAIN TEXT (plainPayload). This is the one write every
// event takes, so it is the one place that guarantees what a reader parses.
func appendTx(ctx context.Context, tx *sql.Tx, e Event) (Event, error) {
	e.Payload = plainPayload(e.Payload)
	if err := dialect.Text(e.ID, e.SessionID, e.Org, e.Kind, e.Actor, e.Payload); err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	var next int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM agent_session_events WHERE session_id=$1`,
		e.SessionID).Scan(&next); err != nil {
		return Event{}, fmt.Errorf("next seq: %w", err)
	}
	e.Seq = next
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_session_events (`+eventCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.ID, e.SessionID, e.Org, e.Seq, e.Kind, e.Actor, e.Payload, e.CreatedAt); err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_sessions SET updated_at=$1 WHERE org=$2 AND id=$3`,
		e.CreatedAt, e.Org, e.SessionID); err != nil {
		return Event{}, fmt.Errorf("bump session: %w", err)
	}
	return e, nil
}

// plainPayload is an event's payload as it is kept: the JSON it was, with every
// string in it read as plain text (sdk/text).
//
// A coding run's log lines are a terminal's output, and `dev` colours them. The
// JSON encoder escapes an ESC faithfully, so what was stored parsed — and a
// reader that unescaped a line to render it had a raw control character in hand,
// which the next JSON reader downstream is required to refuse. So the colour is
// removed where the line becomes a record, once, rather than at every reader. A
// payload that is not JSON at all — no writer here produces one, and every
// reader must still parse what is stored — is kept as a JSON string of its text.
//
// Most payloads have nothing to remove and are kept byte for byte: only one
// that could spell a control character is decoded and written again.
func plainPayload(p string) string {
	if p == "" {
		return p
	}
	if !json.Valid([]byte(p)) {
		b, _ := json.Marshal(text.Plain(p))
		return string(b)
	}
	if !strings.Contains(p, `\u00`) && !strings.Contains(p, `\r`) &&
		!strings.Contains(p, `\b`) && !strings.Contains(p, `\f`) {
		return p
	}
	dec := json.NewDecoder(strings.NewReader(p))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return p
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(plainValue(v)) != nil {
		return p
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// plainValue is v with every string in it, keys included, as plain text.
func plainValue(v any) any {
	switch t := v.(type) {
	case string:
		return text.Plain(t)
	case []any:
		for i := range t {
			t[i] = plainValue(t[i])
		}
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[text.Plain(k)] = plainValue(x)
		}
		return out
	}
	return v
}

// ListEvents returns a session's events in Seq order (optionally only those with
// Seq > since, so a subscriber resumes exactly where it dropped), capped.
func (s *Store) ListEvents(ctx context.Context, org, sessionID string, since int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1001 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=$1 AND session_id=$2 AND seq>$3
		 ORDER BY seq ASC LIMIT $4`, org, sessionID, since, limit)
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

// ReplayEvent pairs a stored Event with its session's visibility metadata so a
// reconnecting SSE subscriber can replay missed events with the exact same
// reader.sees check live bus updates apply.
type ReplayEvent struct {
	Event        Event
	RootID       string
	SessionActor string
	Channel      string
}

// ReplayEvents returns events with Seq > after across an org (optionally scoped
// to a single root session tree) in causal order for SSE cursor replay.
func (s *Store) ReplayEvents(ctx context.Context, org, root string, after int64, limit int) ([]ReplayEvent, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	var (
		rows *sql.Rows
		err  error
	)
	if root != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT e.id, e.session_id, e.org, e.seq, e.kind, e.actor, e.payload, e.created_at,
			       s.root_id, s.actor, s.room
			FROM agent_session_events e
			JOIN agent_sessions s ON s.org = e.org AND s.id = e.session_id
			WHERE e.org = $1 AND s.root_id = $2 AND e.seq > $3
			ORDER BY e.created_at ASC, e.seq ASC
			LIMIT $4`, org, root, after, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT e.id, e.session_id, e.org, e.seq, e.kind, e.actor, e.payload, e.created_at,
			       s.root_id, s.actor, s.room
			FROM agent_session_events e
			JOIN agent_sessions s ON s.org = e.org AND s.id = e.session_id
			WHERE e.org = $1 AND e.seq > $2
			ORDER BY e.created_at ASC, e.seq ASC
			LIMIT $3`, org, after, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("replay events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ReplayEvent
	for rows.Next() {
		var r ReplayEvent
		if err := rows.Scan(
			&r.Event.ID, &r.Event.SessionID, &r.Event.Org, &r.Event.Seq,
			&r.Event.Kind, &r.Event.Actor, &r.Event.Payload, &r.Event.CreatedAt,
			&r.RootID, &r.SessionActor, &r.Channel,
		); err != nil {
			return nil, fmt.Errorf("scan replay event: %w", err)
		}
		out = append(out, r)
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
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=$1 AND session_id=$2
		 ORDER BY seq DESC LIMIT $3`, org, sessionID, limit)
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
		 WHERE e.org=$1 AND s.root_id=$2
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
		`SELECT COUNT(*) FROM agent_session_events WHERE org=$1 AND session_id=$2`,
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
		`SELECT `+eventCols+` FROM agent_session_events WHERE org=$1 AND session_id=$2
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
		`UPDATE agent_sessions SET metered_at=$1 WHERE org=$2 AND id=$3 AND metered_at=$4`,
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
	if err := dialect.Text(p.Phase, p.Activity); err != nil {
		return fmt.Errorf("set progress: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET progress_pct=$1, progress_phase=$2, progress_activity=$3,
		        progress_at=$4, progress_estimated=$5
		 WHERE org=$6 AND id=$7`,
		p.Pct, p.Phase, p.Activity, p.At, bit(p.Estimated), org, id)
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
	if err := dialect.Text(p.Phase, p.Activity); err != nil {
		return false, fmt.Errorf("set estimate: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET progress_pct=$1, progress_phase=$2, progress_activity=$3,
		        progress_at=$4, progress_estimated=$5
		 WHERE org=$6 AND id=$7 AND progress_at=$8`,
		p.Pct, p.Phase, p.Activity, p.At, bit(p.Estimated), org, id, was)
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
// Quiet reports whether this org has nothing any sweep needs: no session still
// running or owing runtime, no resident agent, no published build. A quiet org is
// settled out of the sweeps until it is next asked for (cloud.OrgStore.Settle).
func (s *Store) Quiet(ctx context.Context, org string) (bool, error) {
	var busy bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM agent_sessions WHERE org=$1 AND (
		     ended_at=0
		  OR (run='' AND metered_at>0 AND ended_at>metered_at)
		  OR (published=1 AND project<>'' AND parent_id='')))
		 OR EXISTS(SELECT 1 FROM agents WHERE org=$2 AND execution_mode=$3)`,
		org, org, ModeLongRunning).Scan(&busy)
	return !busy, err
}

func (s *Store) Live(ctx context.Context, org string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE org=$1 AND ended_at=0`, org)
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
		 WHERE org=$1 AND run='' AND box='' AND NOT (host!='' AND target='')
		   AND (ended_at=0 OR (metered_at>0 AND ended_at>metered_at))`, org)
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

// SetSessionBudget writes a session's cap, or its one-way removal.
func (s *Store) SetSessionBudget(ctx context.Context, org, id string, budget int64, removed bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_sessions SET budget_micro_usd=$1, budget_removed=$2 WHERE org=$3 AND id=$4`, budget, bit(removed), org, id)
	if err != nil {
		return fmt.Errorf("set session budget: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errSessionNotFound
	}
	return nil
}

// DeleteSessions removes one or more sessions of org and every event recorded
// against them in a single transaction, returning how many session rows were
// removed. Org is part of every predicate so one tenant can never remove
// another's session or events even if an id collides.
func (s *Store) DeleteSessions(ctx context.Context, org string, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return 0, fmt.Errorf("delete sessions begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var eb binder
	evOrg := eb.bind(org)
	evMarks := make([]string, len(ids))
	for i, id := range ids {
		evMarks[i] = eb.bind(id)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_session_events WHERE org=`+evOrg+` AND session_id IN (`+strings.Join(evMarks, `,`)+`)`,
		eb.args...); err != nil {
		return 0, fmt.Errorf("delete session events: %w", err)
	}

	var sb binder
	sOrg := sb.bind(org)
	sMarks := make([]string, len(ids))
	for i, id := range ids {
		sMarks[i] = sb.bind(id)
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM agent_sessions WHERE org=`+sOrg+` AND id IN (`+strings.Join(sMarks, `,`)+`)`,
		sb.args...)
	if err != nil {
		return 0, fmt.Errorf("delete sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("delete sessions commit: %w", err)
	}
	return n, nil
}
