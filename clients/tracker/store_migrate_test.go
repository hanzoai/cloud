package tracker

import (
	"context"
	"io"
	"testing"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/internal/migratetest"
)

// legacyIssuesDDL is the issues table as it existed BEFORE the polymorphic-spine
// columns (kind/source/repo/ext_ref) were introduced — the shape a production
// tracker.db carries today. A migrate() that indexes repo/kind before ALTER-adding
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
// tracker.db whose issues table predates the spine columns must migrate cleanly,
// not error on "CREATE INDEX ix_issues_org_repo ... no such column: repo".
func TestMigrateOverLegacyIssuesTable(t *testing.T) {
	migratetest.Case{
		Name:      "tracker",
		LegacyDDL: legacyIssuesDDL,
		Open: func(path string) (io.Closer, error) {
			db, err := cek.Open(path)
			if err != nil {
				return nil, err
			}
			db.SetMaxOpenConns(1)
			st, err := openStore(db)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
		// The forward-added spine columns must now exist and be usable — create an
		// issue carrying a repo + kind (the write ix_issues_org_repo /
		// ix_issues_org_kind index), then read it back scoped by org.
		Probe: func(t *testing.T, c io.Closer) {
			st := c.(*Store)
			ctx := context.Background()
			iss, err := st.CreateIssue(ctx, Issue{
				ID: "iss_1", ProjectID: "proj_1", Org: "acme",
				Kind: "pr", Source: "git", Repo: "cloud", ExtRef: "feat/x",
				Title: "t", Status: "backlog", CreatedAt: 1, UpdatedAt: 1,
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
		},
	}.Run(t)
}
