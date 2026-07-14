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
