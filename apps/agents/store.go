package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/mint"
)

var (
	errConflict = errors.New("agents: agent already exists")
	errNotFound = errors.New("agents: agent not found")
)

// Agent is the org-scoped definition of an autonomous worker: a model, a system
// prompt (instructions), and a set of tool names it may call. It lives in its
// org's own database (see Store), and never stores a secret — tool credentials
// live in KMS and are referenced by name at run time.
//
// The bot-lifecycle fields promote an agent from a one-shot callable into a
// long-running bot (per hanzo-agent-bot-architecture: "Bot = Agent + compute +
// long-running"):
//
//   - ExecutionMode: "one-shot" (default; runs only when POSTed) or
//     "long-running" (the scheduler invokes it on Schedule).
//   - Schedule: a 5-field cron expression; required when long-running, ignored
//     otherwise. The scheduler evaluates it once a minute.
//   - ComputeRef: an optional visor machine id the bot is bound to. It is an
//     opaque reference here; binding/lifecycle is owned elsewhere.
//   - ServiceAccountID: an optional IAM agent service-account (<org>-<agent>).
//     When set it is the Actor recorded on scheduled-run billing so an
//     autonomous run is attributable to a principal, not just the org.
type Agent struct {
	ID               string
	Org              string
	Name             string
	Model            string
	Instructions     string
	Description      string
	Tools            []string
	Status           string
	ExecutionMode    string
	Schedule         string
	ComputeRef       string
	ServiceAccountID string

	// How the agent APPEARS, which is the same pair a person and an org carry —
	// iam/pkg/schema's Mark. At most one half is ever set, so a screen never
	// ranks two answers, and neither set means the agent is drawn as its
	// initial. Reusing that type rather than inventing an image field here is
	// what keeps one notion of a face across every subject in the fleet.
	Avatar string
	Emoji  string
	// Where the avatar image came from and under what licence: the page that
	// describes the file (a Wikimedia Commons file page, say) and the licence's
	// short name ("Public domain"). They belong to the image, so both are empty
	// when there is none, and an image replaced by another never keeps them.
	AvatarSource  string
	AvatarLicence string

	CreatedAt int64
	UpdatedAt int64

	// Payer and MeteredAt are the runtime meter's two facts about a RESIDENT bot.
	//
	// A long-running agent is a bot sitting resident, which the published catalog
	// prices exactly as it prices a coding session: "every hour an agent runs
	// bills at agentHourUSD, whether it is writing code, answering in a chat
	// session, or sitting resident as a bot" (@hanzo/plans seats.json). So the
	// row that says an agent is long-running is the row that accrues, and the
	// watermark is stamped on every transition INTO that mode — not at creation —
	// so a one-shot agent's whole life before it became a bot is not a debt.
	//
	// A one-shot agent runs only when it is POSTed, which the per-run fee already
	// charges for, and accrues nothing here.
	Payer     string
	MeteredAt int64

	// The budget: what this agent may spend. Integer micro-USD. Required at
	// creation; a row from before budgets existed carries zero and is uncapped
	// until an update gives it a cap.
	CapMicroUSD     int64  // per period
	MaxTaskMicroUSD int64  // per run
	Period          string // day|week|month
	// What this period has spent, and when it began (UTC, calendar-aligned).
	ConsumedMicroUSD int64
	PeriodStartedAt  int64

	// Parent is the agent that spawned this one, and Lineage every agent above
	// it, nearest first. Both are written once, at creation, and never move: an
	// agent is spawned in the org of the agent that spawns it — the parent is
	// resolved in this file, which holds one org — so a lineage names only this
	// org's agents, and the principal an agent answers to is its parent's by
	// construction. Deleting an ancestor later changes nothing here: the lineage
	// is a fact about how the agent came to exist.
	Parent  string
	Lineage []string

	// CreatedBy is the subject that created the agent, as the identity boundary
	// attested it (principal.SubjectFrom), written once. It decides who may change
	// or delete the agent (manages). Empty is an agent nobody can be shown to have
	// made — seeded, or made before creators were kept — which an admin alone
	// manages.
	CreatedBy string

	// IsPrimary marks the single Primary Bot (at most one per org, default "hanzo")
	// that directs subordinate bots and multi-layer bot groups.
	IsPrimary bool
	// GroupID optionally assigns the bot to a coordination group ("grp_...").
	GroupID string
	// Layer places the bot in the coordination hierarchy (0 = Primary Director,
	// 1 = Group Leads / Tier-1, 2+ = Worker & Consensus Layers).
	Layer int
}

// Execution modes. One-shot agents run only on an explicit POST; long-running
// agents are additionally invoked by the scheduler on their Schedule.
const (
	// ToolsAll in an agent's Tools means "whatever the fleet's MCP server serves",
	// resolved per run rather than enumerated. It exists because the default
	// assistant cannot list a surface that is discovered at runtime and changes
	// whenever a subsystem ships. An agent that declares nothing still gets
	// nothing — that default is its authority, and it is unchanged.
	ToolsAll = "*"

	ModeOneShot     = "one-shot"
	ModeLongRunning = "long-running"
)

// Run is one execution of an agent: the input, the produced output (or error),
// which model served it, and how long it took. Real history — every row is a
// call that actually happened.
type Run struct {
	ID         string
	Org        string
	AgentName  string
	Status     string
	Model      string
	Input      string
	Output     string
	Error      string
	DurationMs int64
	CreatedAt  int64

	// Actor is the "org/sub" identity the run was executed and billed AS. The row
	// already recorded which tenant paid; it never recorded which person asked,
	// so "who ran this" was answerable only from an HTTP audit line that a
	// scheduled or on-behalf run never produces. Empty means there was no person
	// — a schedule or a service token — which is a different fact from unknown.
	Actor string

	// TraceID is the trace this run IS, so the record and its spans are one thing
	// an operator can move between. Without it the run history and the trace store
	// hold two accounts of the same event with no key in common: you can see that
	// a run took nine seconds but not which call spent them.
	//
	// Empty when the process had no tracer installed — an honest "not recorded",
	// never a fabricated id.
	TraceID string
	// MicroUSD is what the run spent, in integer micro-USD.
	MicroUSD int64

	// PromptTokens/CompletionTokens are what the gateway reported for the run's
	// FINAL completion, and ToolCalls is how many tool dispatches it made. They
	// are what the run itself knows. The per-round token spend of a tool loop is
	// the metering ledger's account, joined by this run's id (see
	// infer.ChatRequest.RunID) — recorded there once, rather than re-totalled here
	// into a second number that could disagree with the money.
	PromptTokens     int
	CompletionTokens int
	ToolCalls        int
}

// Store is ONE ORG's agents database — the file at
// {DataDir}/orgs/{slug}/agents.db that cloud.OrgStore opens and caches (HIP-0302
// physical org isolation), holding that org's agents, runs, sessions, events,
// targets and claim keys.
//
// Isolation is now the FILE. Every method still takes and still applies the org
// it is given, and that is deliberate: the predicate costs nothing next to the
// file it already runs in, and it is what makes a mis-resolved store fail closed
// (an empty read) instead of serving a neighbour's rows. The file is the
// boundary; the column is the proof that the boundary held.
type Store struct {
	db *sql.DB
	d  dialect.Dialect
}

// openStore wraps a per-org *sql.DB that cloud.OrgDB has already opened — an
// encrypted SQLite file under the org's own key, or a schema on hanzo-sql — and
// runs the schema migration over it, in one transaction under the store's
// migration lock. Its signature IS cloud.NewOrgStore's open func, so this
// subsystem reaches its stores the same one way the other fifteen do; it never
// resolves a path or opens a driver itself.
//
// Its SQL is the dialect both engines share (sdk/dialect) and its tests
// run on both (internal/sqltest).
func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db, d: dialect.Of(db)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
		return nil, fmt.Errorf("agents: migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agents (
  id                 TEXT PRIMARY KEY,
  org                TEXT NOT NULL,
  name               TEXT NOT NULL,
  model              TEXT NOT NULL DEFAULT '',
  instructions       TEXT NOT NULL DEFAULT '',
  description        TEXT NOT NULL DEFAULT '',
  tools              TEXT NOT NULL DEFAULT '[]',
  status             TEXT NOT NULL DEFAULT 'ready',
  execution_mode     TEXT NOT NULL DEFAULT 'one-shot',
  schedule           TEXT NOT NULL DEFAULT '',
  compute_ref        TEXT NOT NULL DEFAULT '',
  service_account_id TEXT NOT NULL DEFAULT '',
  avatar             TEXT NOT NULL DEFAULT '',
  emoji              TEXT NOT NULL DEFAULT '',
  created_at         BIGINT NOT NULL,
  updated_at         BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_agents_org_name ON agents(org, name);
CREATE INDEX IF NOT EXISTS ix_agents_org_updated ON agents(org, updated_at);

CREATE TABLE IF NOT EXISTS agent_runs (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  agent_name  TEXT NOT NULL,
  status      TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  input       TEXT NOT NULL DEFAULT '',
  output      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL,
  actor             TEXT NOT NULL DEFAULT '',
  trace_id          TEXT NOT NULL DEFAULT '',
  prompt_tokens     BIGINT NOT NULL DEFAULT 0,
  completion_tokens BIGINT NOT NULL DEFAULT 0,
  tool_calls        BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_runs_org_agent_created ON agent_runs(org, agent_name, created_at);
-- The org-wide feed: "what ran here lately", across every agent. The per-agent
-- index cannot serve it — its leading column after org is agent_name, so an
-- org-wide scan by recency would sort every row the org ever produced.
CREATE INDEX IF NOT EXISTS ix_runs_org_created ON agent_runs(org, created_at);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Forward, idempotent migration for databases created before the
	// bot-lifecycle columns existed. Each ADD COLUMN is guarded by a live
	// column-existence check (Dialect.HasColumn), so re-running migrate() on an
	// already-upgraded DB is a no-op and never errors — the DDL above handles
	// fresh DBs, this handles pre-existing ones.
	if err := s.addColumns(ctx, tx, "agents", map[string]string{
		"execution_mode":     "TEXT NOT NULL DEFAULT 'one-shot'",
		"schedule":           "TEXT NOT NULL DEFAULT ''",
		"compute_ref":        "TEXT NOT NULL DEFAULT ''",
		"service_account_id": "TEXT NOT NULL DEFAULT ''",
		// The runtime meter, same refusal-shaped defaults as agent_sessions: no
		// payer means the org's own pool, and no watermark means the clock starts
		// at the first tick that sees the bot resident.
		"payer":      "TEXT NOT NULL DEFAULT ''",
		"metered_at": "BIGINT NOT NULL DEFAULT 0",
		// How the agent appears. Empty is not a missing face, it is the answer
		// "no picture" — the agent is drawn as its initial, which is what every
		// row written before this column existed correctly means.
		"avatar": "TEXT NOT NULL DEFAULT ''",
		"emoji":  "TEXT NOT NULL DEFAULT ''",
		// Where the image came from. Empty is an image nobody said the source of,
		// which every row written before these columns existed is.
		"avatar_source":  "TEXT NOT NULL DEFAULT ''",
		"avatar_licence": "TEXT NOT NULL DEFAULT ''",
		// The budget. Zero cap on a row from before budgets = uncapped, see budget.go.
		"cap_micro_usd":      "BIGINT NOT NULL DEFAULT 0",
		"max_task_micro_usd": "BIGINT NOT NULL DEFAULT 0",
		"period":             "TEXT NOT NULL DEFAULT ''",
		"consumed_micro_usd": "BIGINT NOT NULL DEFAULT 0",
		"period_started_at":  "BIGINT NOT NULL DEFAULT 0",
		// Lineage. Empty is an agent a person defined, which is what every row
		// written before spawning existed is.
		"parent":  "TEXT NOT NULL DEFAULT ''",
		"lineage": "TEXT NOT NULL DEFAULT '[]'",
		// Who made it. Empty is nobody shown to have, which every row written
		// before creators were kept is: an admin's to manage.
		"created_by": "TEXT NOT NULL DEFAULT ''",
		// Primary Bot & multi-layer group hierarchy.
		"is_primary": "BIGINT NOT NULL DEFAULT 0",
		"group_id":   "TEXT NOT NULL DEFAULT ''",
		"layer":      "BIGINT NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	// Same forward, idempotent upgrade for the run attribution columns, so a
	// deployment's existing history keeps working and every run recorded from
	// here on can name its actor, its trace and its token account.
	if err := s.addColumns(ctx, tx, "agent_runs", map[string]string{
		"actor":             "TEXT NOT NULL DEFAULT ''",
		"trace_id":          "TEXT NOT NULL DEFAULT ''",
		"prompt_tokens":     "BIGINT NOT NULL DEFAULT 0",
		"completion_tokens": "BIGINT NOT NULL DEFAULT 0",
		"tool_calls":        "BIGINT NOT NULL DEFAULT 0",
		"micro_usd":         "BIGINT NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	// Every act an agent is charged for, by component, so spend can be read per
	// agent without re-deriving it from runs and residency.
	// One row per due minute of a schedule that fired: the scheduler's claim, shipped
	// fenced before the run, so a minute runs once across every pod that ticks it.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_firings (
		agent TEXT NOT NULL,
		at    BIGINT NOT NULL,
		PRIMARY KEY (agent, at)
	)`); err != nil {
		return fmt.Errorf("migrate: firings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_spend (
		id         TEXT PRIMARY KEY,
		org        TEXT NOT NULL,
		agent_name TEXT NOT NULL,
		run_id     TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		component  TEXT NOT NULL,
		micro_usd  BIGINT NOT NULL,
		created_at BIGINT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS ix_spend_org_agent ON agent_spend(org, agent_name, created_at);`); err != nil {
		return fmt.Errorf("migrate: spend: %w", err)
	}
	// The org-wide recency index, created AFTER the columns above for the same
	// reason the scheduler's partial index is: a legacy DB gains them just now.
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ix_runs_org_created
		ON agent_runs(org, created_at)`); err != nil {
		return fmt.Errorf("migrate: org runs index: %w", err)
	}
	// Partial index for the once-a-minute scheduler scan — created AFTER the
	// lifecycle columns exist (a legacy DB gains them just above), so it selects
	// only the (typically few) scheduled long-running agents instead of
	// full-scanning every org's agents on the single shared SQLite connection.
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ix_agents_scheduled
		ON agents(org, name) WHERE execution_mode='long-running' AND schedule<>''`); err != nil {
		return fmt.Errorf("migrate: scheduled index: %w", err)
	}
	// Live agent-session control-plane tables live in the SAME per-org file —
	// sessions/events are to runs what the subagent tree is to a single call, and
	// keeping them in one file is what lets an event append allocate its sequence
	// and bump its session in ONE transaction.
	if err := s.migrateSessions(ctx, tx); err != nil {
		return err
	}
	// The questions a session's run asks its person (ask.go), beside the session.
	if err := s.migrateQuestions(ctx, tx); err != nil {
		return err
	}
	// A2A tasks (a2a.go), beside the runs that answer them.
	if err := s.migrateA2A(ctx, tx); err != nil {
		return err
	}
	// Agent targets (the #48 dispatch destinations) live in the SAME file too.
	if err := s.migrateTargets(ctx, tx); err != nil {
		return err
	}
	// Per-target claim keys + serving liveness (the #48 route-work machine plane).
	if err := s.migrateClaimKeys(ctx, tx); err != nil {
		return err
	}
	// The folders a machine's owner lets the web run in (folders.go).
	if err := s.migrateFolders(ctx, tx); err != nil {
		return err
	}
	// Rewrite any agent still holding an upstream model name to the Hanzo name.
	if err := s.migrateModel(ctx, tx); err != nil {
		return err
	}
	// Keep the built-in agents the platform seated in step with their source.
	if err := s.migratePersonas(ctx, tx); err != nil {
		return err
	}
	// Studio roster briefs, durable child tasks, and nightly role proposals.
	if err := s.migrateStudio(ctx, tx); err != nil {
		return err
	}
	// Multi-layer bot groups and coordination policies (Leader, Lux Quasar, etc.).
	if err := s.migrateGroups(ctx, tx); err != nil {
		return err
	}
	return nil
}

// migrateModel rewrites every agent whose stored model carries an upstream family
// name to aimodel.ZenModel of it — the data half of the brand boundary. Writes have
// normalized since; this moves the rows that predate that.
//
// IDEMPOTENT: it selects the rows to move by the same predicate that decides where
// they land, so after one pass nothing matches and a re-run is a no-op. Safe to run
// on every store open, which is how it reaches every org's database without anyone
// touching a pod.
//
// REVERSIBLE: the pre-migration model is written to model_snapshot first, keyed by
// agent id with INSERT OR IGNORE, so the FIRST value ever seen is the one kept — a
// second pass can never overwrite the true original. Undo is one statement:
//
//	UPDATE agents SET model = (SELECT model FROM model_snapshot WHERE agent_id = agents.id)
//	 WHERE id IN (SELECT agent_id FROM model_snapshot);
//
// agent_runs is deliberately NOT rewritten. A run is a record of what actually
// happened and falsifying it would be worse than the leak; toRunView guards the
// presentation instead.
func (s *Store) migrateModel(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS model_snapshot (
  agent_id TEXT PRIMARY KEY,
  model    TEXT NOT NULL,
  at       BIGINT NOT NULL
)`); err != nil {
		return fmt.Errorf("migrate: model_snapshot: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT model FROM agents`)
	if err != nil {
		return fmt.Errorf("migrate: scan models: %w", err)
	}
	var stale []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate: scan model: %w", err)
		}
		if aimodel.UpstreamModel(m) {
			stale = append(stale, m)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate: models: %w", err)
	}
	now := time.Now().Unix()
	for _, m := range stale {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO model_snapshot(agent_id, model, at) SELECT id, model, CAST($1 AS BIGINT) FROM agents WHERE model = $2
			 ON CONFLICT (agent_id) DO NOTHING`,
			now, m); err != nil {
			return fmt.Errorf("migrate: snapshot %q: %w", m, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE agents SET model = $1, updated_at = $2 WHERE model = $3`,
			aimodel.ZenModel(m), now, m); err != nil {
			return fmt.Errorf("migrate: rewrite %q: %w", m, err)
		}
	}
	return nil
}

// addColumns adds each missing column to table, idempotently, in a stated order. A
// column already present is skipped; a fresh install (all present from the CREATE)
// is a no-op.
func (s *Store) addColumns(ctx context.Context, tx *sql.Tx, table string, cols map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(cols)) {
		have, err := s.d.HasColumn(ctx, tx, table, name)
		if err != nil {
			return fmt.Errorf("migrate: look for %s.%s: %w", table, name, err)
		}
		if have {
			continue
		}
		// name/def are package-internal literals, never user input — no
		// injection surface. DDL identifiers cannot be parameterized.
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+name+` `+cols[name]); err != nil {
			return fmt.Errorf("migrate: add %s.%s: %w", table, name, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func encodeList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeList(s string) []string {
	if s == "" {
		return nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil
	}
	return xs
}

const agentCols = `id,org,name,model,instructions,description,tools,status,execution_mode,schedule,compute_ref,service_account_id,avatar,emoji,avatar_source,avatar_licence,created_at,updated_at,payer,metered_at,cap_micro_usd,max_task_micro_usd,period,consumed_micro_usd,period_started_at,parent,lineage,created_by,is_primary,group_id,layer`

// runCols is the run projection, named ONCE so the insert, the two reads and the
// legacy fan-out cannot drift apart on a column added to only some of them.
const runCols = `id,org,agent_name,status,model,input,output,error,duration_ms,created_at,actor,trace_id,prompt_tokens,completion_tokens,tool_calls,micro_usd`

func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var tools, lineage string
	var isPrimary int64
	err := sc.Scan(&a.ID, &a.Org, &a.Name, &a.Model, &a.Instructions, &a.Description,
		&tools, &a.Status, &a.ExecutionMode, &a.Schedule, &a.ComputeRef, &a.ServiceAccountID,
		&a.Avatar, &a.Emoji, &a.AvatarSource, &a.AvatarLicence,
		&a.CreatedAt, &a.UpdatedAt, &a.Payer, &a.MeteredAt,
		&a.CapMicroUSD, &a.MaxTaskMicroUSD, &a.Period, &a.ConsumedMicroUSD, &a.PeriodStartedAt,
		&a.Parent, &lineage, &a.CreatedBy, &isPrimary, &a.GroupID, &a.Layer)
	a.Tools, a.Lineage = decodeList(tools), decodeList(lineage)
	a.IsPrimary = isPrimary != 0
	return a, err
}

// normalizeMode is the lowest-layer fail-safe default: an empty execution_mode
// is stored as one-shot so NO path (handler, scheduler, or a direct store call)
// can persist an agent the scheduler would treat ambiguously. The HTTP handler
// also defaults+validates, but this makes the invariant hold at the store.
func normalizeMode(m string) string {
	if strings.TrimSpace(m) == "" {
		return ModeOneShot
	}
	return m
}

// agentText refuses an agent whose text one engine could not store (dialect.Text),
// before a statement is written for it.
func agentText(a Agent) error {
	return dialect.Text(a.ID, a.Org, a.Name, a.Model, a.Instructions, a.Description, a.Status,
		a.ExecutionMode, a.Schedule, a.ComputeRef, a.ServiceAccountID, a.Avatar, a.Emoji,
		a.AvatarSource, a.AvatarLicence, a.Payer, a.Period, a.Parent, a.CreatedBy, a.GroupID)
}

// Create inserts one agent. A UNIQUE(org,name) violation surfaces as errConflict.
// When a.IsPrimary is true, any prior Primary Bot in the org is cleared in the
// same transaction so at most one Primary Bot exists per org.
func (s *Store) Create(ctx context.Context, a Agent) error {
	a.ExecutionMode = normalizeMode(a.ExecutionMode)
	if err := agentText(a); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create agent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if a.IsPrimary {
		if _, err := tx.ExecContext(ctx,
			`UPDATE agents SET is_primary=0 WHERE org=$1 AND is_primary<>0`, a.Org); err != nil {
			return fmt.Errorf("clear prior primary: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO agents (`+agentCols+`) VALUES (`+placeholders(agentCols)+`)`,
		a.ID, a.Org, a.Name, a.Model, a.Instructions, a.Description,
		encodeList(a.Tools), a.Status, a.ExecutionMode, a.Schedule, a.ComputeRef,
		a.ServiceAccountID, a.Avatar, a.Emoji, a.AvatarSource, a.AvatarLicence,
		a.CreatedAt, a.UpdatedAt, a.Payer, a.MeteredAt,
		a.CapMicroUSD, a.MaxTaskMicroUSD, a.Period, a.ConsumedMicroUSD, a.PeriodStartedAt,
		a.Parent, encodeList(a.Lineage), a.CreatedBy, bit(a.IsPrimary), a.GroupID, a.Layer)
	if err != nil {
		if dialect.Unique(err) {
			return errConflict
		}
		return fmt.Errorf("insert agent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create agent: %w", err)
	}
	return nil
}

// Get returns the agent for the exact (org,name) or errNotFound. It is the
// precise name primitive; path-addressed handlers use Resolve (id-or-name).
func (s *Store) Get(ctx context.Context, org, name string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE org=$1 AND name=$2`, org, name)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, errNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("get agent: %w", err)
	}
	return a, nil
}

// ByID returns the agent whose public id is exactly id, or errNotFound. A lineage
// names its ancestors by id, and an id must never resolve to another agent that
// happens to carry that string as its name.
func (s *Store) ByID(ctx context.Context, org, id string) (Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE org=$1 AND id=$2`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, errNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("get agent: %w", err)
	}
	return a, nil
}

// Resolve returns the agent identified by ref within org, matching either its
// public id (the `agent_...` handle create and list hand back) OR its org-unique
// name. This is the ONE lookup every path-addressed handler (get/update/delete/
// run/runs) uses, so a just-created agent is immediately addressable by exactly
// the identifier create/list returned — no id-vs-name split. If a ref somehow
// equals one agent's id and another's name, the id match wins (the stable public
// handle is authoritative). Tenancy is the org filter, so a ref belonging to
// another tenant is errNotFound — fail-closed, never cross-org.
func (s *Store) Resolve(ctx context.Context, org, ref string) (Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE org=$1 AND (id=$2 OR name=$2)
		 ORDER BY (id=$2) DESC LIMIT 1`, org, ref)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, errNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("resolve agent: %w", err)
	}
	return a, nil
}

// List returns every agent for org, most-recently-updated first.
func (s *Store) List(ctx context.Context, org string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE org=$1 ORDER BY updated_at DESC, name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Update overwrites the mutable fields of an existing agent. When a.IsPrimary
// is true, every other agent in the org has is_primary cleared in the same
// transaction so at most one Primary Bot exists per org.
func (s *Store) Update(ctx context.Context, a Agent) error {
	a.ExecutionMode = normalizeMode(a.ExecutionMode)
	if err := agentText(a); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update agent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if a.IsPrimary {
		if _, err := tx.ExecContext(ctx,
			`UPDATE agents SET is_primary=0 WHERE org=$1 AND name<>$2 AND is_primary<>0`, a.Org, a.Name); err != nil {
			return fmt.Errorf("clear prior primary: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE agents SET model=$1,instructions=$2,description=$3,tools=$4,status=$5,
		 execution_mode=$6,schedule=$7,compute_ref=$8,service_account_id=$9,avatar=$10,emoji=$11,
		 avatar_source=$12,avatar_licence=$13,updated_at=$14,
		 cap_micro_usd=$15,max_task_micro_usd=$16,period=$17,
		 is_primary=$18,group_id=$19,layer=$20
		 WHERE org=$21 AND name=$22`,
		a.Model, a.Instructions, a.Description, encodeList(a.Tools), a.Status,
		a.ExecutionMode, a.Schedule, a.ComputeRef, a.ServiceAccountID,
		a.Avatar, a.Emoji, a.AvatarSource, a.AvatarLicence, a.UpdatedAt,
		a.CapMicroUSD, a.MaxTaskMicroUSD, a.Period,
		bit(a.IsPrimary), a.GroupID, a.Layer,
		a.Org, a.Name)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update agent: %w", err)
	}
	return nil
}

// Primary returns the org's single Primary Bot, or errNotFound when none has
// been designated yet.
func (s *Store) Primary(ctx context.Context, org string) (Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE org=$1 AND is_primary<>0 AND status<>$2
		 ORDER BY updated_at DESC LIMIT 1`, org, retired)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, errNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("get primary agent: %w", err)
	}
	return a, nil
}

// SetPrimary promotes name to the org's sole Primary Bot (layer 0), clearing any
// previous Primary Bot in one transaction.
func (s *Store) SetPrimary(ctx context.Context, org, name string, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set primary: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents SET is_primary=0 WHERE org=$1 AND name<>$2 AND is_primary<>0`, org, name); err != nil {
		return fmt.Errorf("clear primary: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE agents SET is_primary=1, layer=0, updated_at=$1 WHERE org=$2 AND name=$3`, now, org, name)
	if err != nil {
		return fmt.Errorf("set primary: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return tx.Commit()
}

// ListLongRunning returns every agent across ALL orgs whose execution_mode is
// long-running and that carries a non-empty schedule — the scheduler's work
// set. It is the ONE cross-org query in this store; the scheduler is a trusted
// in-process subsystem (not a tenant request), and each returned agent carries
// its own Org so every downstream action (run, gate, meter) stays scoped to the
// agent's own tenant.
func (s *Store) ListLongRunning(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentCols+` FROM agents
		 WHERE execution_mode=$1 AND schedule<>'' ORDER BY org, name`, ModeLongRunning)
	if err != nil {
		return nil, fmt.Errorf("list long-running: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountLongRunning returns how many scheduled long-running agents an org has —
// used to cap an org's scheduler footprint at create time.
func (s *Store) CountLongRunning(ctx context.Context, org string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE org=$1 AND execution_mode=$2 AND schedule<>''`,
		org, ModeLongRunning).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count long-running: %w", err)
	}
	return n, nil
}

// Delete removes an agent and its run history. Reports whether a row went.
func (s *Store) Delete(ctx context.Context, org, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_personas WHERE agent_id IN (SELECT id FROM agents WHERE org=$1 AND name=$2)`, org, name); err != nil {
		return false, fmt.Errorf("delete seat: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE org=$1 AND name=$2`, org, name)
	if err != nil {
		return false, fmt.Errorf("delete agent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_runs WHERE org=$1 AND agent_name=$2`, org, name); err != nil {
		return false, fmt.Errorf("delete runs: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// InsertRun records one agent execution.
// firingKept is how long a fired minute's claim is kept: past it, no pod can tick
// that minute again.
const firingKept = 24 * time.Hour

// ClaimFiring takes the due minute at (unix seconds) of agent's schedule, and says
// whether this call took it: false when the minute already fired in this store.
// Claims older than firingKept go with it.
func (s *Store) ClaimFiring(ctx context.Context, agent string, at int64) (bool, error) {
	var taken string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO agent_firings (agent, at) VALUES ($1, $2) ON CONFLICT (agent, at) DO NOTHING RETURNING agent`,
		agent, at).Scan(&taken)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("claim firing: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agent_firings WHERE agent = $1 AND at < $2`,
		agent, at-int64(firingKept/time.Second)); err != nil {
		return false, fmt.Errorf("prune firings: %w", err)
	}
	return err == nil, nil
}

func (s *Store) InsertRun(ctx context.Context, r Run) error {
	if err := dialect.Text(r.ID, r.Org, r.AgentName, r.Status, r.Model, r.Input, r.Output, r.Error, r.Actor, r.TraceID); err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_runs (`+runCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		r.ID, r.Org, r.AgentName, r.Status, r.Model, r.Input, r.Output, r.Error, r.DurationMs, r.CreatedAt,
		r.Actor, r.TraceID, r.PromptTokens, r.CompletionTokens, r.ToolCalls, r.MicroUSD)
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

// scanRun reads one row of the runCols projection. It exists because there are
// two readers of that projection and they were each spelling the column order out
// by hand — which is the drift runCols was named once to prevent, reintroduced one
// layer down. One scanner means a column added to the projection is added to every
// read of it, or to none.
func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	if err := sc.Scan(&r.ID, &r.Org, &r.AgentName, &r.Status, &r.Model, &r.Input,
		&r.Output, &r.Error, &r.DurationMs, &r.CreatedAt,
		&r.Actor, &r.TraceID, &r.PromptTokens, &r.CompletionTokens, &r.ToolCalls, &r.MicroUSD); err != nil {
		return Run{}, fmt.Errorf("scan run: %w", err)
	}
	return r, nil
}

// runScope is whose runs a read returns. A run is the person's who asked for it
// (Run.Actor): its input and output are their words, so a read is scoped to the
// caller's actor and nobody else's — not an org admin's, not a colleague's. A
// run no person asked for is nobody's, and a SuperAdmin alone reads those: one
// recorded with no actor, under the bare org (a service token), or by the
// scheduler (scheduledActor; a person's actor is "<org>/<sub>" and an org name
// has no colon, so the prefix names no person). A scheduled run of an agent is
// also its creator's, who set it running. The zero value reads nothing.
type runScope struct {
	actor     string // billingActor(org, subject); empty when the caller names no stable subject
	creator   string // the caller's subject, whose agents' scheduled runs are theirs
	ownerless bool   // a SuperAdmin: also the runs no person asked for
}

// binder collects a statement's arguments as the statement is written, and hands back
// the placeholder of each: the numbers rise in the order the text is composed, which is
// the order both engines read them in.
type binder struct{ args []any }

func (b *binder) bind(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// bit is a flag as the BIGINT column that holds it: both engines read 1 and 0, and
// PostgreSQL will not take a Go bool for one.
func bit(on bool) int64 {
	if on {
		return 1
	}
	return 0
}

// clause is the scope as SQL over agent_runs for org, its arguments added to b.
func (rs runScope) clause(org string, b *binder) string {
	var terms []string
	if rs.actor != "" {
		terms = append(terms, `actor=`+b.bind(rs.actor))
	}
	if rs.creator != "" {
		terms = append(terms, `(actor LIKE '`+schedulerActor+`:%' AND agent_name IN
			(SELECT name FROM agents WHERE org=`+b.bind(org)+` AND created_by=`+b.bind(rs.creator)+` AND created_by<>''))`)
	}
	if rs.ownerless {
		terms = append(terms, `actor='' OR actor=`+b.bind(org)+` OR actor LIKE '`+schedulerActor+`:%'`)
	}
	if len(terms) == 0 {
		return ` AND FALSE`
	}
	return ` AND (` + strings.Join(terms, ` OR `) + `)`
}

// ListRuns returns the run history for (org,agent) that scope reads, newest
// first, capped.
func (s *Store) ListRuns(ctx context.Context, org, agent string, scope runScope, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var b binder
	q := `SELECT ` + runCols + ` FROM agent_runs WHERE org=` + b.bind(org) + ` AND agent_name=` + b.bind(agent)
	q += scope.clause(org, &b) + ` ORDER BY created_at DESC LIMIT ` + b.bind(limit)
	rows, err := s.db.QueryContext(ctx, q, b.args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunsSince returns the org's runs across ALL agents with created_at >= since,
// newest first, capped. It powers the org-wide surfaces: the recent-activity
// feed (since=0 → the newest runs regardless of age) and the invocation
// histogram (since=windowStart → every run in the window, order-independent for
// bucketing). since<=0 means "no lower bound". Tenancy is the org column, so a
// caller never sees another org's runs, and scope is whose runs in it. Every row
// is a real recorded execution.
func (s *Store) RunsSince(ctx context.Context, org string, scope runScope, since int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 10000 {
		limit = 200
	}
	var b binder
	q := `SELECT ` + runCols + ` FROM agent_runs WHERE org=` + b.bind(org) + ` AND created_at>=` + b.bind(since)
	q += scope.clause(org, &b) + ` ORDER BY created_at DESC LIMIT ` + b.bind(limit)
	rows, err := s.db.QueryContext(ctx, q, b.args...)
	if err != nil {
		return nil, fmt.Errorf("runs since: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountRuns returns how many of an org's agent's runs scope reads (for the list
// rollup).
func (s *Store) CountRuns(ctx context.Context, org, agent string, scope runScope) (int, error) {
	var n int
	var b binder
	q := `SELECT COUNT(*) FROM agent_runs WHERE org=` + b.bind(org) + ` AND agent_name=` + b.bind(agent)
	err := s.db.QueryRowContext(ctx, q+scope.clause(org, &b), b.args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count runs: %w", err)
	}
	return n, nil
}

// ── the runtime meter's store ops ───────────────────────────────────────────────
//
// Three writers, one column each, which is what keeps the watermark honest:
// Create writes it at birth, Stamp writes it at the transition INTO residency,
// and Advance moves it forward. Update deliberately writes NEITHER — it carries a
// struct read at the top of a handler, and a tick landing in between would be
// undone, re-billing the span it had already charged for.

// Stamp starts a resident bot's runtime clock, and names the wallet that pays for
// it. Called on the transition INTO long-running, never at every save: an agent
// that has been one-shot for a month owes nothing for that month, and starting the
// clock at `at` is what says so.
func (s *Store) Stamp(ctx context.Context, org, name, payer string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET payer=$1, metered_at=$2 WHERE org=$3 AND name=$4`, payer, at, org, name)
	if err != nil {
		return fmt.Errorf("stamp agent meter: %w", err)
	}
	return nil
}

// Advance moves one agent's runtime watermark from was to now and reports whether
// THIS caller moved it. The `metered_at=?` in the WHERE clause is the whole of the
// exactly-once property — see cloud.RuntimeCharge, which calls it before it emits.
func (s *Store) Advance(ctx context.Context, org, name string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET metered_at=$1 WHERE org=$2 AND name=$3 AND metered_at=$4`, now, org, name, was)
	if err != nil {
		return false, fmt.Errorf("advance agent meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Resident is every bot this org is keeping alive — the rows the runtime meter
// bills, with NO LIMIT.
//
// It is a wider set than the scheduler's ListLongRunning, which additionally
// requires a schedule: a bot bound to compute with no cron is still resident and
// still costs, and reusing the scheduler's query would have made a bot free by
// leaving its Schedule blank.
func (s *Store) Resident(ctx context.Context, org string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE org=$1 AND execution_mode=$2`, org, ModeLongRunning)
	if err != nil {
		return nil, fmt.Errorf("list resident agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan resident agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── The budget's tallies ──────────────────────────────────────────────────────

// ResetPeriod opens a new window for the agent if the stored one began before
// `start`, zeroing what it spent. A window that already began at `start` is
// left alone, so two callers racing on the same boundary reset once.
func (s *Store) ResetPeriod(ctx context.Context, org, name string, start int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET consumed_micro_usd=0, period_started_at=$1 WHERE org=$2 AND name=$3 AND period_started_at<$4`,
		start, org, name, start)
	if err != nil {
		return fmt.Errorf("reset period: %w", err)
	}
	return nil
}

// Consume records `micros` spent by the agent, on the run and the session it
// ran under when it had them, and as one row of spend by component.
func (s *Store) Consume(ctx context.Context, org, name, runID, sessionID, component string, micros int64) error {
	if micros <= 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents SET consumed_micro_usd=consumed_micro_usd+$1 WHERE org=$2 AND name=$3`, micros, org, name); err != nil {
		return fmt.Errorf("consume agent: %w", err)
	}
	if sessionID != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE agent_sessions SET consumed_micro_usd=consumed_micro_usd+$1 WHERE org=$2 AND id=$3`, micros, org, sessionID); err != nil {
			return fmt.Errorf("consume session: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_spend (id,org,agent_name,run_id,session_id,component,micro_usd,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		mint.ID("spend"), org, name, runID, sessionID, component, micros, time.Now().Unix()); err != nil {
		return fmt.Errorf("record spend: %w", err)
	}
	return tx.Commit()
}

// SpendByComponent sums what the agent has ever spent, per component. A
// component with no spend is absent from the map.
func (s *Store) SpendByComponent(ctx context.Context, org, name string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT component, SUM(micro_usd) FROM agent_spend WHERE org=$1 AND agent_name=$2 GROUP BY component`, org, name)
	if err != nil {
		return nil, fmt.Errorf("spend: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var c string
		var m int64
		if err := rows.Scan(&c, &m); err != nil {
			return nil, fmt.Errorf("spend: %w", err)
		}
		if m > 0 {
			out[c] = m
		}
	}
	return out, rows.Err()
}
