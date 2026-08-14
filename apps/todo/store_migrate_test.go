package todo

import (
	"context"
	"io"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/migratetest"
	"github.com/hanzoai/namespace"
)

// legacyIssuesDDL is the issues table as it existed BEFORE the polymorphic-spine
// columns (kind/source/repo/ext_ref) were introduced — the shape a production
// todo.db carries today. A migrate() that indexes repo/kind before ALTER-adding
// them fails here with "no such column", which fails mount and crashloops the pod.
const legacyIssuesDDL = `
CREATE TABLE issues (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL,
  org          TEXT NOT NULL,
  number       INTEGER NOT NULL,
  title        TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'backlog',
  priority     TEXT NOT NULL DEFAULT 'none',
  assignee     TEXT NOT NULL DEFAULT '',
  labels       TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);`

// TestMigrateOverLegacyIssuesTable reproduces the deploy crashloop: opening a
// todo.db whose issues table predates the spine columns must migrate cleanly,
// not error on "CREATE INDEX ix_issues_org_repo ... no such column: repo".
func TestMigrateOverLegacyIssuesTable(t *testing.T) {
	migratetest.Case{
		Name:      "todo",
		LegacyDDL: legacyIssuesDDL,
		Open: func(dir string) (io.Closer, error) {
			db, err := cloud.OrgDB(dir, namespace.System(), "todo")
			if err != nil {
				return nil, err
			}
			st, err := openStore(db)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
		// The forward-added columns must now exist and be usable — create an issue
		// carrying a repo + kind (the write ix_issues_org_repo / ix_issues_org_kind
		// index) AND a schedule (the write ix_issues_org_project_due index), then
		// read it back scoped by org.
		Probe: func(t *testing.T, c io.Closer) {
			st := c.(*Store)
			ctx := context.Background()
			iss, err := st.CreateIssue(ctx, Issue{
				ID: "iss_1", ProjectID: "proj_1", Org: "acme",
				Kind: "pr", Source: "git", Repo: "cloud", ExtRef: "feat/x",
				Title: "t", Status: "backlog", StartAt: 1700, DueAt: 1900,
				CreatedAt: 1, UpdatedAt: 1,
			})
			if err != nil {
				t.Fatalf("create issue after migrate: %v", err)
			}
			got, err := st.GetIssue(ctx, "acme", "proj_1", iss.Number)
			if err != nil {
				t.Fatalf("get issue after migrate: %v", err)
			}
			if got.Repo != "cloud" || got.Kind != "pr" {
				t.Fatalf("issue spine columns = (repo=%q, kind=%q), want (cloud, pr)", got.Repo, got.Kind)
			}
			if got.StartAt != 1700 || got.DueAt != 1900 {
				t.Fatalf("issue schedule columns = (%d,%d), want (1700,1900)", got.StartAt, got.DueAt)
			}
			// A row that predates the schedule reads as unscheduled rather than as a
			// bar at the epoch, and the timeline filter therefore leaves it alone.
			legacy, err := st.CreateIssue(ctx, Issue{
				ID: "iss_2", ProjectID: "proj_1", Org: "acme",
				Title: "predates the timeline", Status: "backlog", CreatedAt: 1, UpdatedAt: 1,
			})
			if err != nil {
				t.Fatalf("create undated issue: %v", err)
			}
			if legacy.StartAt != 0 || legacy.DueAt != 0 {
				t.Fatalf("undated issue = (%d,%d), want (0,0)", legacy.StartAt, legacy.DueAt)
			}
			timeline, err := st.ListIssues(ctx, "acme", "proj_1", IssueFilter{Scheduled: true})
			if err != nil || len(timeline) != 1 || timeline[0].ID != "iss_1" {
				t.Fatalf("timeline after migrate: %+v err=%v, want just iss_1", timeline, err)
			}
		},
	}.Run(t)
}
