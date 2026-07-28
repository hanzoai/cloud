package agents

import (
	"context"
	"io"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
)

// legacyAgentsDDL is the agents table BEFORE the bot-lifecycle columns
// (execution_mode/schedule/compute_ref/service_account_id) existed.
const legacyAgentsDDL = `
CREATE TABLE agents (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  name         TEXT NOT NULL,
  model        TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL DEFAULT '',
  description  TEXT NOT NULL DEFAULT '',
  tools        TEXT NOT NULL DEFAULT '[]',
  status       TEXT NOT NULL DEFAULT 'ready',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);`

// TestMigrateOverLegacyAgentsTable locks the ordering that keeps ix_agents_scheduled
// — a partial index over the ALTER-added execution_mode + schedule columns — safe on
// a DB whose agents table predates the bot-lifecycle columns.
func TestMigrateOverLegacyAgentsTable(t *testing.T) {
	migratetest.Case{
		Name:      "agents",
		LegacyDDL: legacyAgentsDDL,
		Open: func(path string) (io.Closer, error) {
			st, err := openStore(path)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
		Probe: func(t *testing.T, c io.Closer) {
			st := c.(*Store)
			ctx := context.Background()
			if err := st.Create(ctx, Agent{
				ID: "agent_1", Org: "acme", Name: "bot",
				ExecutionMode: ModeLongRunning, Schedule: "* * * * *",
				CreatedAt: 1, UpdatedAt: 1,
			}); err != nil {
				t.Fatalf("create long-running agent after migrate: %v", err)
			}
			got, err := st.ListLongRunning(ctx)
			if err != nil {
				t.Fatalf("list long-running after migrate: %v", err)
			}
			if len(got) != 1 || got[0].Name != "bot" {
				t.Fatalf("ListLongRunning = %+v, want one agent 'bot'", got)
			}
		},
	}.Run(t)
}

// legacySessionsDDL is the agent_sessions table BEFORE the execution-context columns
// (host/cwd/repo/target) existed — the prior-release schema the migration smoke test
// boots the candidate over.
const legacySessionsDDL = `
CREATE TABLE agent_sessions (
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
);`

// TestMigrateOverLegacySessionsTable locks the ordering that keeps ix_sessions_org_target
// and ix_sessions_org_host safe on a DB whose agent_sessions table predates the
// execution-context columns: the indexes must be created AFTER addColumns adds target
// and host, or the migration fails "no such column: target" over the prior-release
// schema (the v1.800.1-class regression the release smoke guards against).
func TestMigrateOverLegacySessionsTable(t *testing.T) {
	migratetest.Case{
		Name:      "sessions",
		LegacyDDL: legacySessionsDDL,
		Open: func(path string) (io.Closer, error) {
			st, err := openStore(path)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
		Probe: func(t *testing.T, c io.Closer) {
			st := c.(*Store)
			ctx := context.Background()
			// A session carrying a target exercises the ALTER-added target column and
			// the index built over it.
			if err := st.CreateSession(ctx, Session{
				ID: "s1", Org: "acme", Status: StatusRunning,
				Target: "gpu-box", StartedAt: 1, CreatedAt: 1, UpdatedAt: 1,
			}); err != nil {
				t.Fatalf("create session with target after migrate: %v", err)
			}
			got, err := st.GetSession(ctx, "acme", "s1")
			if err != nil {
				t.Fatalf("get session after migrate: %v", err)
			}
			if got.Target != "gpu-box" {
				t.Fatalf("Target = %q, want gpu-box", got.Target)
			}
		},
	}.Run(t)
}
